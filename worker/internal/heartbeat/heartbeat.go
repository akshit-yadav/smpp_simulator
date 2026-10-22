// Package heartbeat publishes the worker's liveness to Redis so the control
// plane can build its worker registry.
package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

var redisUp = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "psg_worker_redis_up",
	Help: "1 if the last heartbeat write to Redis succeeded.",
})

type Beat struct {
	WorkerID    uint8     `json:"worker_id"`
	IP          string    `json:"ip"`
	Version     string    `json:"version"`
	PortRange   string    `json:"port_range"`
	PortExclude []int     `json:"port_exclude"`
	StartedAt   time.Time `json:"started_at"`
	TS          time.Time `json:"ts"`
	// Licence fingerprint: SHA-256 of /etc/machine-id and of the default-route MAC.
	MachineID string `json:"machine_id"`
	MAC       string `json:"mac"`
}

// Key is the Redis key holding a worker's latest heartbeat.
func Key(workerID uint8) string { return fmt.Sprintf("psg:worker:%d:hb", workerID) }

// Run writes a heartbeat every interval until ctx is done. The key expires
// after 3 missed beats. Redis being down is logged once per outage, never fatal.
func Run(ctx context.Context, rdb *redis.Client, b Beat, interval time.Duration, log *slog.Logger) {
	key := Key(b.WorkerID)
	healthy := true
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		b.TS = time.Now().UTC()
		data, _ := json.Marshal(b)
		err := rdb.Set(ctx, key, data, 3*interval).Err()
		switch {
		case err != nil && healthy && ctx.Err() == nil:
			log.Warn("heartbeat failed; redis unreachable", "err", err)
			redisUp.Set(0)
			healthy = false
		case err == nil && !healthy:
			log.Info("heartbeat restored")
			fallthrough
		case err == nil:
			redisUp.Set(1)
			healthy = true
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
