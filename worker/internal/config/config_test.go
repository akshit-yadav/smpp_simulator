package config

import (
	"reflect"
	"testing"
)

func TestParsePortRange(t *testing.T) {
	r, err := ParsePortRange("20000-20999")
	if err != nil || r != (PortRange{20000, 20999}) {
		t.Fatalf("got %v, %v", r, err)
	}
	for _, bad := range []string{"", "20000", "20999-20000", "0-10", "1-70000", "a-b"} {
		if _, err := ParsePortRange(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestParsePortList(t *testing.T) {
	got, err := ParsePortList(" 20022, 20500-20502 ,")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{20022, 20500, 20501, 20502}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, _ := ParsePortList(""); len(got) != 0 {
		t.Fatalf("empty list: got %v", got)
	}
	if _, err := ParsePortList("20022,abc"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadRejectsWorkerIDOutOfRange(t *testing.T) {
	t.Setenv("PSG_WORKER_ID", "256")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for worker ID 256")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PSG_WORKER_ID", "7")
	t.Setenv("PSG_WORKER_IP", "10.0.0.5")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkerID != 7 || c.WorkerIP != "10.0.0.5" || c.PortRange != (PortRange{20000, 20999}) {
		t.Fatalf("unexpected config: %+v", c)
	}
	if !c.EventsEnabled || c.ClickHouseURL != "http://127.0.0.1:8123" || c.ClickHouseDB != "psg" {
		t.Fatalf("unexpected events config: %+v", c)
	}
}

func TestLoadEvents(t *testing.T) {
	t.Setenv("PSG_EVENTS_ENABLED", "false")
	t.Setenv("PSG_CLICKHOUSE_HOST", "ch.local")
	t.Setenv("PSG_CLICKHOUSE_HTTP_PORT", "18123")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.EventsEnabled || c.ClickHouseURL != "http://ch.local:18123" {
		t.Fatalf("unexpected events config: %+v", c)
	}
	t.Setenv("PSG_EVENTS_ENABLED", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for PSG_EVENTS_ENABLED=sometimes")
	}
}

func TestPortAllowed(t *testing.T) {
	c := Config{PortRange: PortRange{20000, 20999}, PortExclude: []int{20022}}
	if c.PortAllowed(20001) != nil || c.PortAllowed(19999) == nil || c.PortAllowed(21000) == nil || c.PortAllowed(20022) == nil {
		t.Fatal("port policy")
	}
}
