// Package config loads worker settings from PSG_* environment variables.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
)

type PortRange struct {
	Min, Max int
}

func (r PortRange) String() string { return fmt.Sprintf("%d-%d", r.Min, r.Max) }

type Config struct {
	WorkerID       uint8
	WorkerIP       string
	MetricsAddr    string
	ControlURL     string
	InternalToken  string
	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	PortRange      PortRange
	PortExclude    []int
	SpillDir       string
	SpillMaxBytes  int64
	EventsEnabled  bool   // write submit/DLR/session events to ClickHouse
	ClickHouseURL  string // HTTP interface
	ClickHouseDB   string
	ClickHouseUser string
	ClickHouseKey  string
	LogLevel       slog.Level
	GatewaysFile   string // static gateway file; empty = get gateways from the control plane
	ConfigCache    string // last config received from the control plane
	ListenHost     string // "" = all interfaces
}

// PortAllowed checks a gateway port against the range and exclusion list.
func (c Config) PortAllowed(port int) error {
	if port < c.PortRange.Min || port > c.PortRange.Max {
		return fmt.Errorf("port %d outside range %s", port, c.PortRange)
	}
	if slices.Contains(c.PortExclude, port) {
		return fmt.Errorf("port %d is excluded", port)
	}
	return nil
}

// Load reads the configuration from the environment. Secrets are never logged.
func Load() (Config, error) {
	var c Config
	var err error

	id, err := strconv.ParseUint(env("PSG_WORKER_ID", "1"), 10, 8)
	if err != nil {
		return c, fmt.Errorf("PSG_WORKER_ID must be 0-255: %w", err)
	}
	c.WorkerID = uint8(id)

	c.WorkerIP = env("PSG_WORKER_IP", "")
	if c.WorkerIP == "" {
		c.WorkerIP = defaultRouteIP()
	}
	c.MetricsAddr = env("PSG_WORKER_METRICS_ADDR", "127.0.0.1:9108")
	c.ControlURL = env("PSG_CONTROL_URL", "http://127.0.0.1:8080")
	c.InternalToken = env("PSG_INTERNAL_TOKEN", "")

	c.RedisAddr = net.JoinHostPort(env("PSG_REDIS_HOST", "127.0.0.1"), env("PSG_REDIS_PORT", "6379"))
	c.RedisPassword = env("PSG_REDIS_PASSWORD", "")
	if c.RedisDB, err = strconv.Atoi(env("PSG_REDIS_DB", "0")); err != nil {
		return c, fmt.Errorf("PSG_REDIS_DB: %w", err)
	}

	if c.PortRange, err = ParsePortRange(env("PSG_PORT_RANGE", "20000-20999")); err != nil {
		return c, fmt.Errorf("PSG_PORT_RANGE: %w", err)
	}
	if c.PortExclude, err = ParsePortList(env("PSG_PORT_EXCLUDE", "")); err != nil {
		return c, fmt.Errorf("PSG_PORT_EXCLUDE: %w", err)
	}

	c.SpillDir = env("PSG_SPILL_DIR", "/var/lib/psg/spill")
	if c.SpillMaxBytes, err = strconv.ParseInt(env("PSG_SPILL_MAX_BYTES", "5368709120"), 10, 64); err != nil {
		return c, fmt.Errorf("PSG_SPILL_MAX_BYTES: %w", err)
	}

	if c.EventsEnabled, err = strconv.ParseBool(env("PSG_EVENTS_ENABLED", "true")); err != nil {
		return c, fmt.Errorf("PSG_EVENTS_ENABLED: %w", err)
	}
	c.ClickHouseURL = "http://" + net.JoinHostPort(env("PSG_CLICKHOUSE_HOST", "127.0.0.1"), env("PSG_CLICKHOUSE_HTTP_PORT", "8123"))
	c.ClickHouseDB = env("PSG_CLICKHOUSE_DATABASE", "psg")
	c.ClickHouseUser = env("PSG_CLICKHOUSE_USER", "default")
	c.ClickHouseKey = env("PSG_CLICKHOUSE_PASSWORD", "")

	c.GatewaysFile = env("PSG_GATEWAYS_FILE", "")
	c.ConfigCache = env("PSG_CONFIG_CACHE", "/var/lib/psg/config-cache.json")
	c.ListenHost = env("PSG_LISTEN_HOST", "")

	if err = c.LogLevel.UnmarshalText([]byte(env("PSG_LOG_LEVEL", "info"))); err != nil {
		return c, fmt.Errorf("PSG_LOG_LEVEL: %w", err)
	}
	return c, nil
}

// ParsePortRange parses "MIN-MAX".
func ParsePortRange(s string) (PortRange, error) {
	lo, hi, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return PortRange{}, fmt.Errorf("want MIN-MAX, got %q", s)
	}
	min, err1 := parsePort(lo)
	max, err2 := parsePort(hi)
	if err1 != nil || err2 != nil || min > max {
		return PortRange{}, fmt.Errorf("invalid port range %q", s)
	}
	return PortRange{Min: min, Max: max}, nil
}

// ParsePortList parses a comma-separated list of ports and ranges, e.g.
// "20022,20500-20502", into individual ports.
func ParsePortList(s string) ([]int, error) {
	var ports []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			r, err := ParsePortRange(part)
			if err != nil {
				return nil, err
			}
			for p := r.Min; p <= r.Max; p++ {
				ports = append(ports, p)
			}
			continue
		}
		p, err := parsePort(part)
		if err != nil {
			return nil, err
		}
		ports = append(ports, p)
	}
	return ports, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return p, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// defaultRouteIP returns the local IP the kernel would use to reach the
// internet. UDP "dial" sends no packets.
func defaultRouteIP() string {
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
