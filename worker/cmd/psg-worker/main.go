// Command psg-worker runs the SMPP listeners for every gateway assigned to
// this worker: bind auth, submit_sm, outcome engine, TPS limits and DLRs.
//
// Gateways come from the control plane (PSG_CONTROL_URL), or from a static
// file when PSG_GATEWAYS_FILE is set (polled every 500 ms).
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
	"github.com/akshit-yadav/smpp_simulator/worker/internal/control"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/events"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/gateway"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/heartbeat"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/licensing"
	"github.com/akshit-yadav/smpp_simulator/worker/internal/server"
	"github.com/akshit-yadav/smpp_simulator/worker/pkg/licence"
)

var version = "dev"

var workerInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "psg_worker_info",
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
	fp := licence.LocalFingerprint(cfg.MachineIDPath)
	pub, err := licence.DecodePublicKey(licence.PublicKey)
	if err != nil {
		log.Error("built-in licence key is broken", "err", err)
		os.Exit(2)
	}
	lic := licensing.New(licensing.Options{
		PublicKey: pub, Fingerprint: fp, ClockPath: cfg.LicenceClock, RequireBoth: cfg.LicenceRequireBoth, Log: log,
	})
	go lic.Run(ctx)
	go heartbeat.Run(ctx, rdb, heartbeat.Beat{
		WorkerID:    cfg.WorkerID,
		IP:          cfg.WorkerIP,
		Version:     version,
		PortRange:   cfg.PortRange.String(),
		PortExclude: cfg.PortExclude,
		StartedAt:   time.Now().UTC(),
		MachineID:   fp.MachineID,
		MAC:         fp.MAC,
	}, 2*time.Second, log)

	// Events get their own context: they stop after the manager, so the
	// session-closed events of the shutdown are written too.
	var rec *events.Recorder
	if cfg.EventsEnabled {
		rec, err = events.New(events.Options{
			WorkerID:      cfg.WorkerID,
			URL:           cfg.ClickHouseURL,
			Database:      cfg.ClickHouseDB,
			User:          cfg.ClickHouseUser,
			Password:      cfg.ClickHouseKey,
			SpillDir:      cfg.SpillDir,
			SpillMaxBytes: cfg.SpillMaxBytes,
			Registerer:    prometheus.DefaultRegisterer,
			Log:           log,
		})
		if err != nil {
			log.Error("events disabled", "err", err)
		}
	}
	recCtx, stopRec := context.WithCancel(context.Background())
	recDone := make(chan struct{})
	go func() {
		if rec != nil {
			rec.Run(recCtx)
		}
		close(recDone)
	}()

	mgr := server.NewManager(server.Options{
		WorkerID:    cfg.WorkerID,
		ListenHost:  cfg.ListenHost,
		PortAllowed: cfg.PortAllowed,
		Redis:       rdb,
		Registerer:  prometheus.DefaultRegisterer,
		Log:         log,
		Events:      rec,
		Licence:     lic,
	})
	mgrDone := make(chan struct{})
	go func() { mgr.Run(ctx); close(mgrDone) }()
	if cfg.GatewaysFile != "" {
		data, err := os.ReadFile(cfg.LicenceFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Error("licence file unreadable; free tier applies", "path", cfg.LicenceFile, "err", err)
		}
		lic.Apply(string(data), 1)
		go gateway.WatchFile(ctx, cfg.GatewaysFile, 500*time.Millisecond, log, func(cfgs []gateway.Config) { mgr.Apply(cfgs) })
	} else {
		src := control.New(control.Options{
			WorkerID:  cfg.WorkerID,
			URL:       cfg.ControlURL,
			Token:     cfg.InternalToken,
			Redis:     rdb,
			CachePath: cfg.ConfigCache,
			PortCheck: func(port int) error { return testBind(cfg, port) },
			Licence:   lic,
			Log:       log,
		}, mgr)
		go src.Run(ctx)
	}

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
		"config_source", configSource(cfg),
		"events", rec != nil,
	)

	<-ctx.Done()
	<-mgrDone // stops listeners and persists queued DLRs
	stopRec()
	<-recDone // writes (or spills) the last events
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

func configSource(cfg config.Config) string {
	if cfg.GatewaysFile != "" {
		return "file:" + cfg.GatewaysFile
	}
	return "control:" + cfg.ControlURL
}

// testBind is the worker's half of the port check: the port must be allowed
// and free on this host right now.
func testBind(cfg config.Config, port int) error {
	if err := cfg.PortAllowed(port); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.ListenHost, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return ln.Close()
}
