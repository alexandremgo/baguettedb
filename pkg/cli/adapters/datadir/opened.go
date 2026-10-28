package datadir

import (
	"fmt"
	"os"
)

// OpenedFiles records files of a data directory that an instance opened, to tell later whether their paths still
// hold them. A directory deleted and recreated, or restored from a backup, holds other files under the same names,
// which an instance keeping the old ones open would never notice. The zero value records nothing.
//
// Files are compared by identity, device and inode. A file the instance keeps open, as its instance store, cannot have
// its inode reused while it does, so its replacement always shows.
type OpenedFiles struct {
	files []openedFile
}

// openedFile is a watched path and the file it held when watched.
type openedFile struct {
	path string
	info os.FileInfo
}

// Watch records the file each of paths holds now; an empty path is skipped.
func (o *OpenedFiles) Watch(paths ...string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("recording %s: %w", path, err)
		}
		o.files = append(o.files, openedFile{path: path, info: info})
	}
	return nil
}

// Changed returns the first watched path that is gone or now holds another file, and false when every path still
// holds the file it held when watched.
func (o *OpenedFiles) Changed() (string, bool) {
	for _, f := range o.files {
		info, err := os.Stat(f.path)
		if err != nil || !os.SameFile(f.info, info) {
			return f.path, true
		}
	}
	return "", false
}
