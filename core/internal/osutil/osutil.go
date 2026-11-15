// Package osutil holds small file and process helpers shared by the
// SoundGit binaries.
package osutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces a file in one step: readers see the old content
// or the new, never a partial write. The file is created with perm.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(perm)
	}
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}
