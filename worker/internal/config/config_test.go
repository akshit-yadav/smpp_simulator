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
	t.Setenv("FSG_WORKER_ID", "256")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for worker ID 256")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("FSG_WORKER_ID", "7")
	t.Setenv("FSG_WORKER_IP", "10.0.0.5")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkerID != 7 || c.WorkerIP != "10.0.0.5" || c.PortRange != (PortRange{20000, 20999}) {
		t.Fatalf("unexpected config: %+v", c)
	}
}
