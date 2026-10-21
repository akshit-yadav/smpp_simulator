// Command fsg-worker runs the SMPP listeners for every gateway assigned to
// this worker: bind auth, submit_sm, outcome engine, TPS limits and DLRs.
// Gateways come from FSG_GATEWAYS_FILE, polled every 500 ms so changes apply within 1 s.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/akshit-yadav/smpp_simulator/worker/internal/config"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/heartbeat"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/server"
)

var version = "dev"

var workerInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "fsg_worker_info",
	Help: "Static worker information.",
}, []string{"worker_id", "ip", "version"})

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(cfg.MetricsAddr))
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	workerInfo.WithLabelValues(strconv.Itoa(int(cfg.WorkerID)), cfg.WorkerIP, version).Set(1)
	go heartbeat.Run(ctx, rdb, heartbeat.Beat{
		WorkerID:  cfg.WorkerID,
		IP:        cfg.WorkerIP,
		Version:   version,
		PortRange: cfg.PortRange.String(),
		StartedAt: time.Now().UTC(),
	}, 2*time.Second, log)

	mgr := server.NewManager(server.Options{
		WorkerID:    cfg.WorkerID,
		ListenHost:  cfg.ListenHost,
		PortAllowed: cfg.PortAllowed,
		Redis:       rdb,
		Registerer:  prometheus.DefaultRegisterer,
		Log:         log,
	})
	mgrDone := make(chan struct{})
	go func() { mgr.Run(ctx); close(mgrDone) }()
	go gateway.WatchFile(ctx, cfg.GatewaysFile, 500*time.Millisecond, log, func(cfgs []gateway.Config) { mgr.Apply(cfgs) })

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server failed", "addr", cfg.MetricsAddr, "err", err)
			stop()
		}
	}()

	log.Info("worker started",
		"version", version,
		"worker_id", cfg.WorkerID,
		"ip", cfg.WorkerIP,
		"port_range", cfg.PortRange.String(),
		"excluded_ports", len(cfg.PortExclude),
		"metrics_addr", cfg.MetricsAddr,
		"redis_addr", cfg.RedisAddr,
		"control_url", cfg.ControlURL,
		"gateways_file", cfg.GatewaysFile,
	)

	<-ctx.Done()
	<-mgrDone // stops listeners and persists queued DLRs
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("worker stopped")
}

// healthcheck probes /healthz on the metrics listener. Used by the container
// healthcheck, since the runtime image has no shell or curl.
func healthcheck(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
