package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/server"
)

const workerID = 7

type fakeManager struct {
	mu      sync.Mutex
	applied [][]gateway.Config
}

func (f *fakeManager) Apply(c []gateway.Config) []server.ApplyResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, c)
	return nil
}

func (f *fakeManager) Status() map[int64]server.GatewayStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int64]server.GatewayStatus{}
	if n := len(f.applied); n > 0 {
		for _, c := range f.applied[n-1] {
			out[c.ID] = server.GatewayStatus{State: "listening", Port: c.Port}
		}
	}
	return out
}

func (f *fakeManager) HasErrors() bool { return false }

func (f *fakeManager) last() []gateway.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) == 0 {
		return nil
	}
	return f.applied[len(f.applied)-1]
}

// fakeControl serves the internal API with a config whose port can change.
type fakeControl struct {
	version atomic.Int64
	port    atomic.Int64
	down    atomic.Bool
	pulls   atomic.Int64
}

func (fc *fakeControl) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if fc.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case fmt.Sprintf("/internal/v1/workers/%d/config/version", workerID):
			json.NewEncoder(w).Encode(map[string]any{"version": fc.version.Load()})
		case fmt.Sprintf("/internal/v1/workers/%d/config", workerID):
			fc.pulls.Add(1)
			gw := map[string]any{"id": 1, "name": "a", "port": fc.port.Load(), "system_id": "a", "password": "pw"}
			bad := map[string]any{"id": 2, "name": "bad", "port": 1, "system_id": "this-is-way-too-long"}
			json.NewEncoder(w).Encode(map[string]any{"worker_id": workerID, "version": fc.version.Load(), "gateways": []any{gw, bad}})
		default:
			http.NotFound(w, r)
		}
	})
}

func testRedis(t *testing.T) *redis.Client {
	addr := os.Getenv("FSG_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("FSG_TEST_REDIS_ADDR not set")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 13})
	rdb.FlushDB(context.Background())
	t.Cleanup(func() { rdb.FlushDB(context.Background()); rdb.Close() })
	return rdb
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func start(t *testing.T, rdb *redis.Client, fc *fakeControl, cache string, mgr *fakeManager) context.CancelFunc {
	srv := httptest.NewServer(fc.handler(t))
	t.Cleanup(srv.Close)
	src := New(Options{
		WorkerID: workerID, URL: srv.URL, Token: "tok", Redis: rdb, CachePath: cache, Log: slog.Default(),
		PortCheck: func(port int) error {
			if port == 20099 {
				return errors.New("address already in use")
			}
			return nil
		},
		VersionCheck: 200 * time.Millisecond,
	}, mgr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { src.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func TestSyncOnStartAndOnPublish(t *testing.T) {
	rdb := testRedis(t)
	fc := &fakeControl{}
	fc.version.Store(1)
	fc.port.Store(20001)
	mgr := &fakeManager{}
	cache := filepath.Join(t.TempDir(), "cache.json")
	start(t, rdb, fc, cache, mgr)

	waitFor(t, "initial sync", func() bool { return len(mgr.last()) == 1 })
	if got := mgr.last()[0]; got.Port != 20001 || got.MaxBinds != 10 || string(got.Password) != "pw" {
		t.Fatalf("applied %+v", got) // invalid gateway 2 is skipped, defaults filled in
	}
	waitFor(t, "status report", func() bool {
		v, _ := rdb.HGet(context.Background(), "fsg:gwstatus:7", "1").Result()
		return v != ""
	})

	// A published change is applied quickly.
	fc.version.Store(2)
	fc.port.Store(20002)
	begin := time.Now()
	rdb.Publish(context.Background(), "fsg:cfg:7", `{"gateway_id":1,"version":2,"action":"update"}`)
	waitFor(t, "update", func() bool { return mgr.last()[0].Port == 20002 })
	if d := time.Since(begin); d > time.Second {
		t.Fatalf("change took %v", d)
	}

	// The cache holds the latest config, readable only by the worker.
	st, err := os.Stat(cache)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("cache: %v %v", st, err)
	}
}

func TestVersionCheckCatchesMissedPublish(t *testing.T) {
	rdb := testRedis(t)
	fc := &fakeControl{}
	fc.version.Store(1)
	fc.port.Store(20001)
	mgr := &fakeManager{}
	start(t, rdb, fc, "", mgr)
	waitFor(t, "initial sync", func() bool { return len(mgr.last()) == 1 })

	fc.version.Store(5) // no publish
	fc.port.Store(20005)
	waitFor(t, "version check", func() bool { return mgr.last()[0].Port == 20005 })
}

func TestCachedConfigWhenControlDown(t *testing.T) {
	rdb := testRedis(t)
	cache := filepath.Join(t.TempDir(), "cache.json")
	fc := &fakeControl{}
	fc.version.Store(3)
	fc.port.Store(20003)
	mgr := &fakeManager{}
	cancel := start(t, rdb, fc, cache, mgr)
	waitFor(t, "sync", func() bool { return len(mgr.last()) == 1 })
	cancel()
	time.Sleep(50 * time.Millisecond)

	// A restarted worker with the control plane down serves the cached config.
	fc2 := &fakeControl{}
	fc2.down.Store(true)
	mgr2 := &fakeManager{}
	start(t, rdb, fc2, cache, mgr2)
	waitFor(t, "cached config", func() bool { return len(mgr2.last()) == 1 })
	if mgr2.last()[0].Port != 20003 {
		t.Fatalf("cached port %d", mgr2.last()[0].Port)
	}
}

func TestPortCheckCommand(t *testing.T) {
	rdb := testRedis(t)
	fc := &fakeControl{}
	fc.version.Store(1)
	fc.port.Store(20001)
	start(t, rdb, fc, "", &fakeManager{})
	ctx := context.Background()
	waitFor(t, "subscription", func() bool {
		n, _ := rdb.PubSubNumSub(ctx, "fsg:cmd:7").Result()
		return n["fsg:cmd:7"] == 1
	})

	for port, wantOK := range map[int]bool{20010: true, 20099: false} {
		id := fmt.Sprintf("req%d", port)
		rdb.Publish(ctx, "fsg:cmd:7", fmt.Sprintf(`{"type":"port_check","id":"%s","port":%d}`, id, port))
		res, err := rdb.BLPop(ctx, 2*time.Second, "fsg:reply:"+id).Result()
		if err != nil {
			t.Fatal(err)
		}
		var reply struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		json.Unmarshal([]byte(res[1]), &reply)
		if reply.OK != wantOK || (!wantOK && reply.Error == "") {
			t.Fatalf("port %d: %+v", port, reply)
		}
	}
}
