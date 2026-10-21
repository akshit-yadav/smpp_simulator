package gateway

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const sample = `{
  "defaults": {"max_binds": 4, "dlr": {"min_ms": 100, "max_ms": 200}},
  "gateways": [
    {"id": 1, "name": "a", "port": 20001, "system_id": "vendora", "password": "pw1"},
    {"id": 2, "name": "b", "port": 20002, "system_id": "vendorb", "password": "pw2",
     "max_binds": 1, "bind_types": ["trx"], "dlr": {"enabled": false},
     "outcome": {"mode": "weighted", "outcomes": [
        {"dlr_status": "DELIVRD", "dlr_error": "000", "weight_bp": 9500},
        {"dlr_status": "UNDELIV", "dlr_error": "T01", "weight_bp": 500}]}}
  ]
}`

func TestParseMergesDefaults(t *testing.T) {
	cfgs, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	a, b := cfgs[0], cfgs[1]
	if a.MaxBinds != 4 || a.DLR.MinMS != 100 || a.DLR.MaxMS != 200 || !a.DLR.Enabled || a.DLR.Timing != TimingRandom {
		t.Fatalf("a did not inherit defaults: %+v", a)
	}
	if len(a.BindTypes) != 3 || a.Outcome.Mode != "fixed" || a.Status != StatusActive {
		t.Fatalf("a builtin defaults: %+v", a)
	}
	if b.MaxBinds != 1 || b.DLR.Enabled || b.DLR.MinMS != 100 || !b.AllowsBind(BindTRX) || b.AllowsBind(BindTX) {
		t.Fatalf("b overrides: %+v", b)
	}
	if b.Outcome.Mode != "weighted" || len(b.Outcome.Outcomes) != 2 {
		t.Fatalf("b outcome: %+v", b.Outcome)
	}
	// The default outcome slice must not be shared between gateways.
	if &a.Outcome.Outcomes[0] == &Builtin.Outcome.Outcomes[0] {
		t.Fatal("defaults aliased")
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"long system id": `{"id":1,"name":"a","port":20001,"system_id":"abcdefghijklmnop","password":"x"}`,
		"long password":  `{"id":1,"name":"a","port":20001,"system_id":"a","password":"123456789"}`,
		"bad bind type":  `{"id":1,"name":"a","port":20001,"system_id":"a","bind_types":["xx"]}`,
		"bad weights":    `{"id":1,"name":"a","port":20001,"system_id":"a","outcome":{"mode":"weighted","outcomes":[{"dlr_status":"DELIVRD","weight_bp":1}]}}`,
		"no name":        `{"id":1,"port":20001,"system_id":"a"}`,
	}
	for name, gw := range cases {
		if _, err := Parse([]byte(`{"gateways":[` + gw + `]}`)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	dup := `{"gateways":[{"id":1,"name":"a","port":20001,"system_id":"a"},{"id":2,"name":"b","port":20001,"system_id":"b"}]}`
	if _, err := Parse([]byte(dup)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate port: %v", err)
	}
}

func TestSecretNeverPrints(t *testing.T) {
	c := Config{Password: "hunter2"}
	var sb strings.Builder
	slog.New(slog.NewJSONHandler(&sb, nil)).Info("x", "pw", c.Password)
	out := sb.String() + fmt.Sprintf("%v %+v %#v %s", c.Password, c, c, c.Password)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("password leaked: %s", out)
	}
}
