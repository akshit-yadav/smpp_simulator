package events

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

var errSpillFull = errors.New("event spill is full")

// spillFile is one RowBinary batch on disk, named <seq>.<table>.<rows>.rb.
// seq (Unix nanoseconds, zero-padded) orders the files and, with the worker
// ID, forms the batch's deduplication token.
type spillFile struct {
	seq   int64
	table string
	rows  int
	size  int64
}

func (f spillFile) name() string { return fmt.Sprintf("%020d.%s.%d.rb", f.seq, f.table, f.rows) }

func parseSpillName(name string) (spillFile, bool) {
	parts := strings.Split(strings.TrimSuffix(name, ".rb"), ".")
	if !strings.HasSuffix(name, ".rb") || len(parts) != 3 {
		return spillFile{}, false
	}
	seq, err1 := strconv.ParseInt(parts[0], 10, 64)
	rows, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return spillFile{}, false
	}
	return spillFile{seq: seq, table: parts[1], rows: rows}, true
}

// spill is a directory of batches waiting for ClickHouse, oldest first. Only
// the recorder's writer goroutine uses it; bytes is read by the metrics.
type spill struct {
	dir   string
	max   int64
	files []spillFile
	bytes atomic.Int64
}

func openSpill(dir string, max int64) (*spill, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("event spill: %w", err)
	}
	entries, err := os.ReadDir(dir) // sorted by name, so by seq
	if err != nil {
		return nil, fmt.Errorf("event spill: %w", err)
	}
	s := &spill{dir: dir, max: max}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") { // a write cut short
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		f, ok := parseSpillName(e.Name())
		info, err := e.Info()
		if !ok || err != nil {
			continue
		}
		f.size = info.Size()
		s.files = append(s.files, f)
		s.bytes.Add(f.size)
	}
	return s, nil
}

// write stores a batch durably (fsync, then rename into place).
func (s *spill) write(f spillFile, data []byte) error {
	if s.bytes.Load()+int64(len(data)) > s.max {
		return errSpillFull
	}
	path := filepath.Join(s.dir, f.name())
	tmp, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}
	if err != nil {
		_ = os.Remove(path + ".tmp")
		return err
	}
	f.size = int64(len(data))
	s.files = append(s.files, f)
	s.bytes.Add(f.size)
	return nil
}

func (s *spill) read(f spillFile) ([]byte, error) { return os.ReadFile(filepath.Join(s.dir, f.name())) }

// remove deletes the oldest file, f, once it is in ClickHouse.
func (s *spill) remove(f spillFile) {
	_ = os.Remove(filepath.Join(s.dir, f.name()))
	s.drop()
}

// quarantine renames the oldest file, f, to .bad so replay can move on.
func (s *spill) quarantine(f spillFile) {
	path := filepath.Join(s.dir, f.name())
	_ = os.Rename(path, path+".bad")
	s.drop()
}

func (s *spill) drop() {
	s.bytes.Add(-s.files[0].size)
	s.files[0] = spillFile{}
	s.files = s.files[1:]
	if len(s.files) == 0 {
		s.files = nil
	}
}
