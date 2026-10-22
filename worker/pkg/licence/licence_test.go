package licence

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func sample() Licence {
	return Licence{
		LicenceID: "HWPL-T-1", Licensee: "Test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(30 * 24 * time.Hour),
		MaxGateways: 50, MaxTPS: 25000,
		Machines: []Machine{{MachineID: "aa", MAC: "bb", Label: "host"}},
	}
}

func TestSignAndParse(t *testing.T) {
	pub, priv := testKeys(t)
	file, err := Sign(sample(), priv)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Parse(file, pub)
	if err != nil || l.MaxTPS != 25000 || l.Machines[0].Label != "host" {
		t.Fatalf("parsed %+v, %v", l, err)
	}

	other, _ := testKeys(t)
	if _, err := Parse(file, other); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong key: %v", err)
	}
	// Raising max_tps in the payload breaks the signature.
	var env envelope
	_ = json.Unmarshal(file, &env)
	payload, _ := base64.StdEncoding.DecodeString(env.Payload)
	env.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(payload), "25000", "99999", 1)))
	tampered, _ := json.Marshal(env)
	if _, err := Parse(tampered, pub); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := Parse([]byte(`{"hello": 1}`), pub); !errors.Is(err, ErrFormat) {
		t.Fatalf("not a licence: %v", err)
	}
}

func TestCheck(t *testing.T) {
	l := sample()
	full, partial, none := Fingerprint{"aa", "bb"}, Fingerprint{"aa", "zz"}, Fingerprint{"yy", "zz"}
	if l.Match(full) != "full" || l.Match(partial) != "partial" || l.Match(none) != "none" {
		t.Fatal("match")
	}
	if l.Check(full, now, time.Time{}, true) != nil || l.Check(partial, now, time.Time{}, false) != nil {
		t.Fatal("valid licence refused")
	}
	if l.Check(partial, now, time.Time{}, true) == nil {
		t.Fatal("partial match accepted with require-both")
	}
	if l.Check(none, now, time.Time{}, false) == nil {
		t.Fatal("other machine accepted")
	}
	if l.Check(full, l.ExpiresAt.Add(time.Second), time.Time{}, false) == nil {
		t.Fatal("expired licence accepted")
	}
	if l.Check(full, now, now.Add(25*time.Hour), false) == nil {
		t.Fatal("clock rollback of 25 h accepted")
	}
	if l.Check(full, now, now.Add(23*time.Hour), false) != nil {
		t.Fatal("clock rollback of 23 h refused")
	}
}

func TestLocalFingerprint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "machine-id")
	_ = os.WriteFile(p, []byte("0123456789abcdef\n"), 0o644)
	fp := LocalFingerprint(p)
	if fp.MachineID != digest("0123456789abcdef") || len(fp.MachineID) != 64 {
		t.Fatalf("machine id digest %q", fp.MachineID)
	}
	if LocalFingerprint(filepath.Join(t.TempDir(), "missing")).MachineID != "" {
		t.Fatal("missing file gave a digest")
	}
}

func TestBuiltInPublicKey(t *testing.T) {
	if _, err := DecodePublicKey(PublicKey); err != nil {
		t.Fatal(err)
	}
}
