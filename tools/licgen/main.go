// Command licgen manages PSG licences (HWPL internal only):
//
//	licgen keygen -out DIR          new Ed25519 key pair (private key 0600; never commit it)
//	licgen sign -key FILE -in FILE  sign a licence (fields as JSON) and print the licence file
//	licgen verify -in FILE [-pub B64]
//	licgen fingerprint              this machine's fingerprint, as a machines[] entry
//
// The public key printed by keygen goes in worker/pkg/licence (PublicKey)
// and control/psg_control/licence.py (PUBLIC_KEY).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/pkg/licence"
)

const usage = `usage:
  licgen keygen -out DIR
  licgen sign -key PRIVATE_KEY_FILE -in FIELDS.json [-out FILE]
  licgen verify -in LICENCE_FILE [-pub BASE64_PUBLIC_KEY]
  licgen fingerprint [-machine-id /etc/machine-id] [-label NAME]

FIELDS.json: {"licence_id", "licensee", "issued_at" (optional, default now),
"expires_at", "max_gateways", "max_tps", "machines": [{"machine_id", "mac",
"label"}], "features"}. Times are RFC 3339, e.g. 2027-10-22T00:00:00Z.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "fingerprint":
		err = fingerprint(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "licgen:", err)
		os.Exit(1)
	}
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "directory for the key files")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("-out is required")
	}
	priv := filepath.Join(*out, "psg-licence-ed25519.key")
	if _, err := os.Stat(priv); err == nil {
		return fmt.Errorf("%s exists; refusing to overwrite a signing key", priv)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(priv, []byte(base64.StdEncoding.EncodeToString(key.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(filepath.Join(*out, "psg-licence-ed25519.pub"), []byte(pubB64+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("private key: %s (keep it secret, back it up)\npublic key:  %s\n", priv, pubB64)
	return nil
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("not a licgen private key")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "", "private key file from keygen")
	in := fs.String("in", "", "licence fields (JSON)")
	out := fs.String("out", "", "write the licence here (default stdout)")
	_ = fs.Parse(args)
	key, err := readPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	var l licence.Licence
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return fmt.Errorf("%s: %w", *in, err)
	}
	if l.IssuedAt.IsZero() {
		l.IssuedAt = time.Now().UTC().Truncate(time.Second)
	}
	switch {
	case l.LicenceID == "" || l.Licensee == "":
		return errors.New("licence_id and licensee are required")
	case !l.ExpiresAt.After(l.IssuedAt):
		return errors.New("expires_at must be after issued_at")
	case l.MaxGateways <= 0 || l.MaxTPS <= 0:
		return errors.New("max_gateways and max_tps must be positive")
	case len(l.Machines) == 0:
		return errors.New("machines must list at least one fingerprint (licgen fingerprint on the host)")
	}
	file, err := licence.Sign(l, key)
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = os.Stdout.Write(append(file, '\n'))
		return err
	}
	return os.WriteFile(*out, append(file, '\n'), 0o644)
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	in := fs.String("in", "", "licence file")
	pubB64 := fs.String("pub", licence.PublicKey, "public key (default: the one built into PSG)")
	_ = fs.Parse(args)
	pub, err := licence.DecodePublicKey(*pubB64)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	l, err := licence.Parse(b, pub)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(l, "", "  ")
	fmt.Printf("signature OK\n%s\n", out)
	return nil
}

func fingerprint(args []string) error {
	fs := flag.NewFlagSet("fingerprint", flag.ExitOnError)
	path := fs.String("machine-id", "/etc/machine-id", "machine ID file")
	label := fs.String("label", "", "label for the machines[] entry (default: hostname)")
	_ = fs.Parse(args)
	fp := licence.LocalFingerprint(*path)
	if fp.MachineID == "" && fp.MAC == "" {
		return errors.New("no machine ID or default-route MAC found")
	}
	if *label == "" {
		*label, _ = os.Hostname()
	}
	out, _ := json.MarshalIndent(licence.Machine{MachineID: fp.MachineID, MAC: fp.MAC, Label: *label}, "", "  ")
	fmt.Println(string(out))
	return nil
}
