// Command licgen signs PSG licences with the Ed25519 private key (HWPL internal
// only). Implemented in M8. The private key must never be committed.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "licgen: not implemented yet (milestone M8)")
	os.Exit(1)
}
