package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic is the single owner for mutable metadata/config replacement.
// Readers see either the complete old file or the complete new file.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return fmt.Errorf("目标文件路径为空")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	closeWithError := func(writeErr error) error {
		_ = tmp.Close()
		return writeErr
	}
	if err := tmp.Chmod(mode); err != nil {
		return closeWithError(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return closeWithError(err)
	}
	if err := tmp.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceFileAtomic(tmpPath, path)
}
