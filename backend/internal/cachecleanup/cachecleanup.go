package cachecleanup

import (
	"os"
	"path/filepath"
)

// Result summarizes cache cleanup for a single Chromium profile root.
type Result struct {
	FilesRemoved int   `json:"filesRemoved"`
	DirsRemoved  int   `json:"dirsRemoved"`
	BytesRemoved int64 `json:"bytesRemoved"`
	Errors       int   `json:"errors"`
}

var profileSubroots = []string{"Default"}

var cacheRelativePaths = []string{
	"Cache",
	"Code Cache",
	"GPUCache",
	"ShaderCache",
	"DawnCache",
	"GrShaderCache",
	"GraphiteDawnCache",
	"Service Worker/CacheStorage",
	"Service Worker/ScriptCache",
	"IndexedDB",
	"Local Storage",
	"Session Storage",
	"WebStorage",
	"Storage",
	"File System",
	"blob_storage",
	"Cookies",
	"Cookies-journal",
	"Network/Cookies",
	"Network/Cookies-journal",
}

// CleanProfileRoot removes cache/storage/cookie artifacts from a Chromium
// user-data root while intentionally preserving identity/profile files such as
// Preferences, Bookmarks and History.
func CleanProfileRoot(profileRoot string) (Result, error) {
	var total Result
	for _, subroot := range profileSubroots {
		base := filepath.Join(profileRoot, subroot)
		for _, rel := range cacheRelativePaths {
			path := filepath.Join(base, filepath.FromSlash(rel))
			res := removePath(path)
			total.FilesRemoved += res.FilesRemoved
			total.DirsRemoved += res.DirsRemoved
			total.BytesRemoved += res.BytesRemoved
			total.Errors += res.Errors
		}
	}
	return total, nil
}

func removePath(path string) Result {
	var res Result
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return res
		}
		res.Errors++
		return res
	}
	if !info.IsDir() {
		res.FilesRemoved = 1
		res.BytesRemoved = info.Size()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			res.Errors++
			res.FilesRemoved = 0
			res.BytesRemoved = 0
		}
		return res
	}
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			res.Errors++
			return nil
		}
		if d.IsDir() {
			res.DirsRemoved++
			return nil
		}
		res.FilesRemoved++
		if info, err := d.Info(); err == nil {
			res.BytesRemoved += info.Size()
		}
		return nil
	})
	if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
		res.Errors++
	}
	return res
}
