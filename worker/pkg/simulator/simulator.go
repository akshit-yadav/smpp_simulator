// Package simulator holds what PSG's own SMPP client simulator (psg-tester)
// and the worker agree on.
package simulator

// AddressRange is the address_range the tester sends in its binds. The
// worker tags such sessions' events as simulator traffic, which reports
// leave out unless asked.
const AddressRange = "PSG-TESTER"
