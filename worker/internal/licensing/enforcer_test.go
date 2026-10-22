package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/pkg/licence"
)

var here = licence.Fingerprint{MachineID: "m1", MAC: "a1"}

func signed(t *testing.T, priv ed25519.PrivateKey, mod func(*licence.Licence)) string {
	t.Helper()
	l := licence.Licence{
		LicenceID: "L1", Licensee: "T", IssuedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(24 * time.Hour),
		MaxGateways: 40, MaxTPS: 30000, Machines: []licence.Machine{{MachineID: "m1", MAC: "a1"}},
	}
	if mod != nil {
		mod(&l)
	}
	b, err := licence.Sign(l, priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEnforcer(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	clock := filepath.Join(t.TempDir(), "clock")
	e := New(Options{PublicKey: pub, Fingerprint: here, ClockPath: clock})
	if st := e.Status(); st.Licensed || st.MaxTPS != licence.FreeTPS || st.MaxGateways != licence.FreeGateways || st.ShareTPS != licence.FreeTPS {
		t.Fatalf("free tier: %+v", st)
	}

	e.Apply(signed(t, priv, nil), 2)
	if st := e.Status(); !st.Licensed || st.MaxGateways != 40 || st.ShareTPS != 15000 {
		t.Fatalf("licensed, 2 workers: %+v", st)
	}
	e.SetShare(20000)
	if st := e.Status(); st.ShareTPS != 20000 {
		t.Fatalf("share: %+v", st)
	}
	e.SetShare(1e9) // never above the cap this worker verified
	if st := e.Status(); st.ShareTPS != 30000 {
		t.Fatalf("share over the cap: %+v", st)
	}

	for name, mod := range map[string]func(*licence.Licence){
		"other machine": func(l *licence.Licence) { l.Machines = []licence.Machine{{MachineID: "x", MAC: "y"}} },
		"expired":       func(l *licence.Licence) { l.ExpiresAt = time.Now().Add(-time.Minute) },
	} {
		e.Apply(signed(t, priv, mod), 1)
		if st := e.Status(); st.Licensed || st.MaxTPS != licence.FreeTPS || st.Reason == "" {
			t.Fatalf("%s: %+v", name, st)
		}
	}
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	e.Apply(signed(t, otherPriv, nil), 1)
	if e.Status().Licensed {
		t.Fatal("licence signed with another key accepted")
	}

	// The clock high-water mark survives a restart; going back 2 days expires the licence.
	future := time.Now().Add(48 * time.Hour)
	New(Options{PublicKey: pub, Fingerprint: here, ClockPath: clock, Now: func() time.Time { return future }})
	e2 := New(Options{PublicKey: pub, Fingerprint: here, ClockPath: clock})
	e2.Apply(signed(t, priv, func(l *licence.Licence) { l.ExpiresAt = time.Now().Add(72 * time.Hour) }), 1)
	if st := e2.Status(); st.Licensed {
		t.Fatalf("clock rollback ignored: %+v", st)
	}
}

func TestBucketCapsRate(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	e := New(Options{PublicKey: pub, Fingerprint: here})
	e.SetShare(50)
	ok := 0
	for range 200 {
		if e.Allow(1) {
			ok++
		}
	}
	if ok < 50 || ok > 52 { // one second of burst
		t.Fatalf("allowed %d of 200 at 50/s", ok)
	}
	if (*Enforcer)(nil).Allow(5) != true {
		t.Fatal("nil enforcer must allow")
	}
}
