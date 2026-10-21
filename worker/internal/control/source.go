// Package control keeps the worker's gateways in sync with the control plane:
//
//   - On start it applies the config cached on disk, so traffic flows even if
//     the control plane is down.
//   - It pulls the full config from the internal API when a change is
//     published on fsg:cfg:<worker_id>, when (re)subscribing, and when the
//     60 s version check finds a newer version.
//   - It answers commands on fsg:cmd:<worker_id> (port_check) and reports
//     each gateway's listener state to the fsg:gwstatus:<worker_id> hash.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/server"
)

type Options struct {
	WorkerID  uint8
	URL       string // control plane base URL
	Token     string // FSG_INTERNAL_TOKEN
	Redis     *redis.Client
	CachePath string
	PortCheck func(port int) error // range/exclusion policy plus a test bind
	Log       *slog.Logger

	VersionCheck time.Duration // default 60 s
}

// Manager is the part of server.Manager the source drives.
type Manager interface {
	Apply([]gateway.Config) []server.ApplyResult
	Status() map[int64]server.GatewayStatus
	HasErrors() bool
}

type Source struct {
	opt     Options
	mgr     Manager
	log     *slog.Logger
	http    *http.Client
	version int64
	configs []gateway.Config
}

// Config is what the internal API returns.
type Config struct {
	WorkerID int               `json:"worker_id"`
	Version  int64             `json:"version"`
	Gateways []json.RawMessage `json:"gateways"`
}

func New(opt Options, mgr Manager) *Source {
	if opt.VersionCheck == 0 {
		opt.VersionCheck = 60 * time.Second
	}
	return &Source{opt: opt, mgr: mgr, log: opt.Log, http: &http.Client{Timeout: 10 * time.Second}}
}

func (s *Source) channels() (cfg, cmd string) {
	id := strconv.Itoa(int(s.opt.WorkerID))
	return "fsg:cfg:" + id, "fsg:cmd:" + id
}

// Run blocks until ctx is done.
func (s *Source) Run(ctx context.Context) {
	if c, err := s.readCache(); err == nil {
		s.log.Info("applying cached config", "version", c.Version, "gateways", len(c.Gateways))
		s.apply(ctx, c, false)
	} else if !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("config cache unreadable", "path", s.opt.CachePath, "err", err)
	}

	trigger := make(chan struct{}, 1)
	poke := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	poke()
	go s.listen(ctx, poke)

	check := time.NewTicker(s.opt.VersionCheck)
	defer check.Stop()
	status := time.NewTicker(15 * time.Second)
	defer status.Stop()
	var retry <-chan time.Time
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
		case <-retry:
		case <-status.C:
			s.reportStatus(ctx)
			continue
		case <-check.C:
			if v, err := s.fetchVersion(ctx); err == nil && v == s.version && !s.mgr.HasErrors() {
				continue
			}
		}
		time.Sleep(50 * time.Millisecond) // coalesce bursts of changes
		select {
		case <-trigger:
		default:
		}
		if err := s.sync(ctx); err != nil {
			s.log.Warn("config sync failed; keeping current config", "err", err, "retry_in", backoff.String())
			retry = time.After(backoff)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		retry, backoff = nil, time.Second
	}
}

// listen subscribes to config notifications and commands. A (re)subscription
// triggers a full sync, which covers changes missed while disconnected.
func (s *Source) listen(ctx context.Context, poke func()) {
	cfgCh, cmdCh := s.channels()
	ps := s.opt.Redis.Subscribe(ctx, cfgCh, cmdCh)
	go func() { <-ctx.Done(); ps.Close() }()
	for msg := range ps.ChannelWithSubscriptions() {
		switch m := msg.(type) {
		case *redis.Subscription:
			if m.Kind == "subscribe" && m.Channel == cfgCh {
				poke()
			}
		case *redis.Message:
			if m.Channel == cfgCh {
				poke()
			} else {
				s.handleCommand(ctx, m.Payload)
			}
		}
	}
}

type command struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Port int    `json:"port"`
}

func (s *Source) handleCommand(ctx context.Context, payload string) {
	var c command
	if err := json.Unmarshal([]byte(payload), &c); err != nil || c.ID == "" {
		s.log.Warn("bad command", "payload", payload)
		return
	}
	reply := map[string]any{"ok": true}
	switch c.Type {
	case "port_check":
		if err := s.opt.PortCheck(c.Port); err != nil {
			reply = map[string]any{"ok": false, "error": err.Error()}
		}
	default:
		reply = map[string]any{"ok": false, "error": "unknown command " + c.Type}
	}
	data, _ := json.Marshal(reply)
	key := "fsg:reply:" + c.ID
	pipe := s.opt.Redis.TxPipeline()
	pipe.RPush(ctx, key, data)
	pipe.Expire(ctx, key, 30*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		s.log.Warn("command reply failed", "type", c.Type, "err", err)
	}
}

func (s *Source) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opt.URL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.opt.Token)
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

func (s *Source) fetchVersion(ctx context.Context) (int64, error) {
	var v struct {
		Version int64 `json:"version"`
	}
	err := s.get(ctx, fmt.Sprintf("/internal/v1/workers/%d/config/version", s.opt.WorkerID), &v)
	return v.Version, err
}

func (s *Source) sync(ctx context.Context) error {
	var c Config
	if err := s.get(ctx, fmt.Sprintf("/internal/v1/workers/%d/config", s.opt.WorkerID), &c); err != nil {
		return err
	}
	if c.Version == s.version && !s.mgr.HasErrors() {
		return nil
	}
	s.apply(ctx, &c, true)
	return nil
}

// apply decodes and applies a config; one invalid gateway does not block the rest.
func (s *Source) apply(ctx context.Context, c *Config, cache bool) {
	cfgs := make([]gateway.Config, 0, len(c.Gateways))
	for _, raw := range c.Gateways {
		g, err := gateway.Decode(gateway.Builtin, raw)
		if err != nil {
			s.log.Error("gateway config rejected", "err", err)
			continue
		}
		cfgs = append(cfgs, g)
	}
	s.mgr.Apply(cfgs)
	if c.Version != s.version {
		s.log.Info("config applied", "version", c.Version, "gateways", len(cfgs))
	}
	s.version, s.configs = c.Version, cfgs
	if cache {
		if err := s.writeCache(c); err != nil {
			s.log.Warn("config cache write failed", "path", s.opt.CachePath, "err", err)
		}
	}
	s.reportStatus(ctx)
}

func (s *Source) reportStatus(ctx context.Context) {
	key := "fsg:gwstatus:" + strconv.Itoa(int(s.opt.WorkerID))
	pipe := s.opt.Redis.TxPipeline()
	pipe.Del(ctx, key)
	for id, st := range s.mgr.Status() {
		data, _ := json.Marshal(st)
		pipe.HSet(ctx, key, strconv.FormatInt(id, 10), data)
	}
	_, _ = pipe.Exec(ctx) // best effort; refreshed every 15 s
}

func (s *Source) readCache() (*Config, error) {
	if s.opt.CachePath == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(s.opt.CachePath)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// writeCache stores the config atomically, readable only by the worker (it
// contains gateway passwords).
func (s *Source) writeCache(c *Config) error {
	if s.opt.CachePath == "" {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.opt.CachePath), 0o700); err != nil {
		return err
	}
	tmp := s.opt.CachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.opt.CachePath)
}
