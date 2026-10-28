package datadir

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// servedFileName is the file in the data directory that records the bdb serve running on it.
const servedFileName = "serve.json"

// servedJSON is a domain.ServedInstance as the file holds it.
type servedJSON struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

// ServedFile records the bdb serve running on a data directory, for other commands to find it.
type ServedFile struct {
	path string
}

// NewServedFile creates a ServedFile over the data directory dataDir.
func NewServedFile(dataDir string) *ServedFile {
	return &ServedFile{path: filepath.Join(dataDir, servedFileName)}
}

// Record records served, owner-only, in place of any record. The file is replaced at once, so a reader sees the
// previous record or this one, never part of either.
func (f *ServedFile) Record(served domain.ServedInstance) (err error) {
	b, err := json.Marshal(servedJSON(served))
	if err != nil {
		return fmt.Errorf("encoding the served instance: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), "."+servedFileName+".*")
	if err != nil {
		return fmt.Errorf("recording the served instance: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmp.Name()))
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		return errors.Join(fmt.Errorf("recording the served instance: %w", err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("recording the served instance: %w", err)
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		return fmt.Errorf("recording the served instance: %w", err)
	}
	return nil
}

// Current returns the recorded bdb serve, and false when none is recorded or its process is gone, as after a server
// killed before it could forget its record.
func (f *ServedFile) Current() (domain.ServedInstance, bool, error) {
	served, ok, err := f.read()
	if err != nil || !ok || !processRunning(served.PID) {
		return domain.ServedInstance{}, false, err
	}
	return served, true, nil
}

// Forget removes the record when it is the one of process pid, so a server stopping never removes the record of
// another one started on the same data directory since. No record is not an error.
func (f *ServedFile) Forget(pid int) error {
	served, ok, err := f.read()
	if err != nil || !ok || served.PID != pid {
		return err
	}
	if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("forgetting the served instance: %w", err)
	}
	return nil
}

// read returns the record as the file holds it, and false when there is none.
func (f *ServedFile) read() (domain.ServedInstance, bool, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return domain.ServedInstance{}, false, nil
	}
	if err != nil {
		return domain.ServedInstance{}, false, fmt.Errorf("reading the served instance: %w", err)
	}
	var served servedJSON
	if err := json.Unmarshal(b, &served); err != nil {
		return domain.ServedInstance{}, false, fmt.Errorf("reading the served instance in %s: %w", f.path, err)
	}
	return domain.ServedInstance(served), true, nil
}

// processRunning reports whether a process pid exists. Signal 0 checks without signalling: EPERM means the process
// exists but belongs to another user. A pid reused by another program since reads as running.
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
