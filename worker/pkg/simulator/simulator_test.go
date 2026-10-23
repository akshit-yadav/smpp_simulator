package simulator

import (
	"testing"
	"time"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/msgid"
)

func TestMessageTime(t *testing.T) {
	id := msgid.New(7).Next()
	for _, s := range []string{msgid.Hex(id), msgid.Decimal(id)} {
		got, ok := MessageTime(s)
		if !ok || time.Since(got).Abs() > time.Second {
			t.Errorf("%s: %v %v", s, got, ok)
		}
	}
	for _, bad := range []string{"", "1", "abc", "zz", "123456789abcdef0123"} {
		if _, ok := MessageTime(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
}
