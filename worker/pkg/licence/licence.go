// Package licence reads, verifies and signs PSG licences.
//
// A licence file is a JSON envelope:
//
//	{"format": "psg-licence-1", "payload": "<base64 JSON>", "signature": "<base64>"}
//
// The signature is Ed25519 over the exact payload bytes, so verifying needs
// no JSON canonicalisation (the worker in Go and the control plane in Python
// check the same bytes). Only the public key ships with PSG; tools/licgen
// holds the private key (HWPL only).
package licence

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const Format = "psg-licence-1"

// PublicKey verifies licences (base64, raw 32-byte Ed25519 key). The matching
// private key is HWPL's; see tools/licgen.
var PublicKey = "t2XblfGnqZ798s9cEm1KKmnvtcT0+txOKEIPZz/sSx4="

// The free tier, without a valid licence. Change it here (and in the control
// plane's licence.py) when it is confirmed.
const (
	FreeGateways = 10
	FreeTPS      = 1000
)

// Licence is the signed content.
type Licence struct {
	LicenceID   string    `json:"licence_id"`
	Licensee    string    `json:"licensee"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	MaxGateways int       `json:"max_gateways"`
	MaxTPS      int       `json:"max_tps"`
	Machines    []Machine `json:"machines"`
	Features    []string  `json:"features"`
}

// Machine is a licensed host: SHA-256 (hex) of /etc/machine-id and of the MAC
// of the interface carrying the default route.
type Machine struct {
	MachineID string `json:"machine_id"`
	MAC       string `json:"mac"`
	Label     string `json:"label,omitempty"`
}

type envelope struct {
	Format    string `json:"format"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

var (
	ErrFormat    = errors.New("not a PSG licence file")
	ErrSignature = errors.New("licence signature is invalid")
)

// DecodePublicKey decodes a base64 public key.
func DecodePublicKey(b64 string) (ed25519.PublicKey, error) {
	k, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(k) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key")
	}
	return ed25519.PublicKey(k), nil
}

// Parse checks the signature and decodes the licence. It does not check
// expiry or machines; see Check.
func Parse(data []byte, pub ed25519.PublicKey) (*Licence, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Format != Format {
		return nil, ErrFormat
	}
	payload, err1 := base64.StdEncoding.DecodeString(env.Payload)
	sig, err2 := base64.StdEncoding.DecodeString(env.Signature)
	if err1 != nil || err2 != nil {
		return nil, ErrFormat
	}
	if !ed25519.Verify(pub, payload, sig) {
		return nil, ErrSignature
	}
	var l Licence
	if err := json.Unmarshal(payload, &l); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	if l.MaxGateways < 0 || l.MaxTPS < 0 || l.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("%w: limits or expiry missing", ErrFormat)
	}
	return &l, nil
}

// Sign builds a licence file.
func Sign(l Licence, priv ed25519.PrivateKey) ([]byte, error) {
	payload, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(envelope{
		Format:    Format,
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
	}, "", "  ")
}

// Fingerprint identifies a host (SHA-256 hex digests; empty if unknown).
type Fingerprint struct {
	MachineID string `json:"machine_id"`
	MAC       string `json:"mac"`
}

// Match reports how a host matches the licence: "full" (both digests),
// "partial" (one of them) or "none".
func (l *Licence) Match(fp Fingerprint) string {
	best := "none"
	for _, m := range l.Machines {
		id := fp.MachineID != "" && m.MachineID == fp.MachineID
		mac := fp.MAC != "" && m.MAC == fp.MAC
		switch {
		case id && mac:
			return "full"
		case id || mac:
			best = "partial"
		}
	}
	return best
}

// Check reports why the licence does not apply on this host at now, or nil.
// highWater is the latest time seen before (clock rollback of more than
// 24 h counts as expired); requireBoth refuses a partial machine match.
func (l *Licence) Check(fp Fingerprint, now, highWater time.Time, requireBoth bool) error {
	if now.After(l.ExpiresAt) {
		return fmt.Errorf("licence %s expired on %s", l.LicenceID, l.ExpiresAt.Format(time.DateOnly))
	}
	if !highWater.IsZero() && now.Before(highWater.Add(-24*time.Hour)) {
		return errors.New("the clock went back more than 24 hours; the licence counts as expired")
	}
	switch l.Match(fp) {
	case "none":
		return errors.New("this machine is not in the licence")
	case "partial":
		if requireBoth {
			return errors.New("this machine only partly matches the licence (machine ID or MAC changed)")
		}
	}
	return nil
}

// LocalFingerprint fingerprints this host: SHA-256 of the machine ID file
// and of the MAC of the interface carrying the default route.
func LocalFingerprint(machineIDPath string) Fingerprint {
	var fp Fingerprint
	if b, err := os.ReadFile(machineIDPath); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			fp.MachineID = digest(id)
		}
	}
	if mac := defaultRouteMAC(); mac != "" {
		fp.MAC = digest(mac)
	}
	return fp
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// defaultRouteMAC returns the lowercase MAC of the interface the default
// route uses (Linux), or "".
func defaultRouteMAC() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) > 2 && f[1] == "00000000" {
			if ifc, err := net.InterfaceByName(f[0]); err == nil && len(ifc.HardwareAddr) > 0 {
				return strings.ToLower(ifc.HardwareAddr.String())
			}
		}
	}
	return ""
}
