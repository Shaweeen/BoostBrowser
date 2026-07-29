//go:build !windows

package fsutil

import "os"

func replaceFileAtomic(source, destination string) error {
	return os.Rename(source, destination)
}
