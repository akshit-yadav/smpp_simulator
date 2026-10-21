package server

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/outcome"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/ratelimit"
)

// runtimeConfig is an immutable, compiled gateway config. Changes swap in a
// new one atomically.
type runtimeConfig struct {
	gateway.Config
	picker   *outcome.Picker
	gwBucket *ratelimit.Bucket // used when TPS scope is "gateway"
}

// Gateway is one running SMPP listener and its sessions.
type Gateway struct {
	m     *Manager
	id    int64
	cfg   atomic.Pointer[runtimeConfig]
	ln    net.Listener
	stats *Stats

	mu        sync.Mutex
	sessions  map[*Session]struct{}
	bound     int
	stopped   bool
	receivers atomic.Pointer[[]*Session] // copy-on-write; bound RX/TRX sessions
	rr        atomic.Uint64
}

func listen(host string, port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, &PortError{Port: port, Err: err}
	}
	return ln, nil
}

func (m *Manager) newGateway(rc *runtimeConfig, ln net.Listener) *Gateway {
	gw := &Gateway{
		m:        m,
		id:       rc.ID,
		ln:       ln,
		stats:    m.statsFor(rc.ID),
		sessions: map[*Session]struct{}{},
	}
	gw.cfg.Store(rc)
	gw.receivers.Store(&[]*Session{})
	go gw.acceptLoop()
	return gw
}

func (gw *Gateway) config() *runtimeConfig { return gw.cfg.Load() }

func (gw *Gateway) update(rc *runtimeConfig) { gw.cfg.Store(rc) }

func (gw *Gateway) acceptLoop() {
	for {
		conn, err := gw.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			gw.m.log.Warn("accept failed", "gateway_id", gw.id, "err", err)
			continue
		}
		s := newSession(gw, conn)
		gw.mu.Lock()
		if gw.stopped {
			gw.mu.Unlock()
			conn.Close()
			return
		}
		gw.sessions[s] = struct{}{}
		gw.mu.Unlock()
		go s.serve()
	}
}

// stop closes the listener and unbinds every session.
func (gw *Gateway) stop(reason string) {
	gw.mu.Lock()
	if gw.stopped {
		gw.mu.Unlock()
		return
	}
	gw.stopped = true
	sessions := make([]*Session, 0, len(gw.sessions))
	for s := range gw.sessions {
		sessions = append(sessions, s)
	}
	gw.mu.Unlock()

	gw.ln.Close()
	for _, s := range sessions {
		s.unbindAndClose()
	}
	gw.m.log.Info("gateway stopped", "gateway_id", gw.id, "reason", reason, "sessions", len(sessions))
}

// reserveBind checks max binds and counts the session as bound.
func (gw *Gateway) reserveBind(maxBinds int) bool {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.stopped || (maxBinds > 0 && gw.bound >= maxBinds) {
		return false
	}
	gw.bound++
	return true
}

func (gw *Gateway) addReceiver(s *Session) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	old := *gw.receivers.Load()
	next := make([]*Session, len(old), len(old)+1)
	copy(next, old)
	next = append(next, s)
	gw.receivers.Store(&next)
}

// removeSession forgets a closed session.
func (gw *Gateway) removeSession(s *Session, wasBound bool) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	delete(gw.sessions, s)
	if wasBound {
		gw.bound--
	}
	old := *gw.receivers.Load()
	next := make([]*Session, 0, len(old))
	for _, r := range old {
		if r != s {
			next = append(next, r)
		}
	}
	gw.receivers.Store(&next)
}

func (gw *Gateway) hasReceivers() bool { return len(*gw.receivers.Load()) > 0 }

// nextReceiver round-robins over bound RX/TRX sessions; nil if none.
func (gw *Gateway) nextReceiver() *Session {
	rs := *gw.receivers.Load()
	if len(rs) == 0 {
		return nil
	}
	return rs[gw.rr.Add(1)%uint64(len(rs))]
}
