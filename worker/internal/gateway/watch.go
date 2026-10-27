package gateway

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"
)

// WatchFile loads path now and again whenever its size or mtime changes
// (checked every interval), calling apply with each valid version. An invalid
// file is logged and ignored, so the last good config keeps running.
func WatchFile(ctx context.Context, path string, interval time.Duration, log *slog.Logger, apply func([]Config)) {
	var lastMod time.Time
	var lastSize int64 = -1
	missingLogged := false
	check := func() {
		st, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			if !missingLogged {
				log.Warn("gateway file not found; running with no gateways", "path", path)
				missingLogged = true
			}
			return
		}
		if err != nil {
			log.Error("gateway file stat failed", "path", path, "err", err)
			return
		}
		missingLogged = false
		if st.ModTime().Equal(lastMod) && st.Size() == lastSize {
			return
		}
		lastMod, lastSize = st.ModTime(), st.Size()
		cfgs, err := LoadFile(path)
		if err != nil {
			log.Error("gateway file invalid; keeping previous config", "path", path, "err", err)
			return
		}
		log.Info("gateway file loaded", "path", path, "gateways", len(cfgs))
		apply(cfgs)
	}

	check()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}
