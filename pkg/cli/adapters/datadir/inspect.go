// Package datadir handles what the command line itself keeps or checks in the data directory: files one command
// writes for others to read, and whether the files an instance opened are still the directory's. It never reads or
// writes a bdb's data nor the instance's.
package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// Inspect describes the data directory dataDir and the key file, instance store and bdb files directory the settings
// place at key, store and databases, from their metadata alone: which exist, whether the key is readable by its owner
// only, and the sizes of the instance store and the bdb files, journal files included. It opens and creates nothing;
// the source of the directory and any environment override are the caller's to set.
func Inspect(dataDir, key, store, databases string) (domain.DataDirInfo, error) {
	d := domain.DataDirInfo{
		Path:          dataDir,
		Key:           domain.DataFile{Path: key},
		InstanceStore: domain.DataFile{Path: store},
		Databases:     domain.DataFile{Path: databases},
	}
	var err error
	if d.Exists, err = exists(dataDir); err != nil {
		return domain.DataDirInfo{}, err
	}
	for _, f := range []*domain.DataFile{&d.Key, &d.InstanceStore, &d.Databases} {
		if f.Exists, err = exists(f.Path); err != nil {
			return domain.DataDirInfo{}, err
		}
	}

	if d.Key.Exists {
		st, err := os.Stat(key)
		if err != nil {
			return domain.DataDirInfo{}, fmt.Errorf("reading the key file: %w", err)
		}
		d.KeyOwnerOnly = st.Mode().Perm()&0o077 == 0
	}
	d.InstanceStoreBytes = sizeOf(store, store+"-wal", store+"-shm")
	if d.Databases.Exists {
		files, err := filepath.Glob(filepath.Join(databases, "*.db"))
		if err != nil {
			return domain.DataDirInfo{}, fmt.Errorf("listing the bdb files: %w", err)
		}
		d.BdbFiles = len(files)
		for _, f := range files {
			d.BdbBytes += sizeOf(f, f+"-wal", f+"-shm")
		}
	}
	return d, nil
}

// exists reports whether path exists, an error other than its absence being returned.
func exists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	return true, nil
}

// sizeOf returns the total size of the regular files among paths that exist.
func sizeOf(paths ...string) int64 {
	var total int64
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			total += st.Size()
		}
	}
	return total
}
