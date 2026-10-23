// Package simulator holds what PSG's own SMPP client simulator (psg-tester)
// and the worker agree on.
package simulator

import (
	"strconv"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/msgid"
)

// AddressRange is the address_range the tester sends in its binds. The
// worker tags such sessions' events as simulator traffic, which reports
// leave out unless asked.
const AddressRange = "PSG-TESTER"

// MessageTime returns when PSG issued a message ID, as found in a
// submit_sm_resp or a DLR: hex (at most 16 digits) or decimal (the DLR "id:"
// option; an ID from worker 1 or above has at least 17 digits). ok is false
// for an ID PSG did not issue.
func MessageTime(id string) (t time.Time, ok bool) {
	base := 16
	if len(id) > 16 {
		base = 10
	}
	n, err := strconv.ParseUint(id, base, 64)
	if err != nil || msgid.WorkerID(n) == 0 {
		return time.Time{}, false
	}
	t = msgid.Time(n)
	return t, t.Before(time.Now().Add(time.Hour))
}
