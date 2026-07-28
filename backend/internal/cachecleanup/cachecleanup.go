package cachecleanup

import (
	"os"
	"path/filepath"
	"strings"
)

// Result summarizes disposable cache cleanup for a Chromium user-data root.
type Result struct {
	FilesRemoved int   `json:"filesRemoved"`
	DirsRemoved  int   `json:"dirsRemoved"`
	BytesRemoved int64 `json:"bytesRemoved"`
	Errors       int   `json:"errors"`
}

// Only content that Chromium can regenerate belongs in these lists. In
// particular, never add Cookies, IndexedDB, Local Storage, Service Worker,
// Extension State or other origin/extension storage here: wallet and login
// state live in those locations.
var disposableDirNames = map[string]struct{}{
	"cache":                          {},
	"code cache":                     {},
	"gpucache":                       {},
	"shadercache":                    {},
	"dawncache":                      {},
	"dawngraphitecache":              {},
	"grshadercache":                  {},
	"graphitedawncache":              {},
	"media cache":                    {},
	"videodecodestats":               {},
	"jumplisticons":                  {},
	"jumplisticonsold":               {},
	"crashpad":                       {},
	"crash reports":                  {},
	"browsermetrics":                 {},
	"component_crx_cache":            {},
	"optimization_guide_model_store": {},
	"optimization_guide_prediction_model_downloads": {},
}

var disposableFileNames = map[string]struct{}{
	"chrome_debug.log":  {},
	"debug.log":         {},
	"favicons":          {},
	"favicons-journal":  {},
	"top sites":         {},
	"top sites-journal": {},
}

var disposableDirPaths = []string{
	"Cache",
	"Network/Cache",
	"Code Cache",
	"GPUCache",
	"ShaderCache",
	"DawnCache",
	"DawnGraphiteCache",
	"GrShaderCache",
	"GraphiteDawnCache",
	"Media Cache",
	"VideoDecodeStats",
	"JumpListIcons",
	"JumpListIconsOld",
	"Crashpad",
	"Crash Reports",
	"BrowserMetrics",
	"component_crx_cache",
	"optimization_guide_model_store",
	"optimization_guide_prediction_model_downloads",
}

var disposableFilePaths = []string{
	"chrome_debug.log",
	"debug.log",
	"Favicons",
	"Favicons-journal",
	"Top Sites",
	"Top Sites-journal",
}

// IsDisposableRelativePath is shared by cleanup and backup creation so a file
// cannot be treated as essential by one subsystem and disposable by another.
// It intentionally matches exact path components only; LevelDB *.log files
// inside wallet/extension storage are therefore preserved.
func IsDisposableRelativePath(rel string) bool {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." || clean == "" {
		return false
	}
	parts := strings.Split(clean, "/")
	start := 0
	foundProfileDir := false
	for i, part := range parts {
		if isChromiumProfileDir(part) {
			start = i + 1
			foundProfileDir = true
			break
		}
	}
	// Backup scopes usually add one environment-directory component before the
	// Chromium user-data root (for example profile-id/GPUCache).
	if !foundProfileDir && len(parts) >= 2 {
		second := strings.ToLower(strings.TrimSpace(parts[1]))
		if _, ok := disposableDirNames[second]; ok {
			return true
		}
		if second == "network" && len(parts) >= 3 && strings.EqualFold(strings.TrimSpace(parts[2]), "Cache") {
			return true
		}
		if len(parts) == 2 {
			if _, ok := disposableFileNames[second]; ok {
				return true
			}
		}
	}
	scoped := parts[start:]
	if len(scoped) == 0 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(scoped[0]))
	if _, ok := disposableDirNames[first]; ok {
		return true
	}
	if len(scoped) >= 2 && first == "network" && strings.EqualFold(strings.TrimSpace(scoped[1]), "Cache") {
		return true
	}
	if len(scoped) == 1 {
		_, ok := disposableFileNames[first]
		return ok
	}
	_, ok := disposableFileNames[strings.ToLower(strings.TrimSpace(scoped[len(scoped)-1]))]
	return ok && len(scoped) == 1
}

func isChromiumProfileDir(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "default" || name == "guest profile" || strings.HasPrefix(name, "profile ")
}

// CleanProfileRoot removes only explicitly allow-listed, regenerable browser
// artifacts. Cookies, login sessions, fingerprints, extension/wallet storage,
// bookmarks, history and preferences are preserved.
func CleanProfileRoot(profileRoot string) (Result, error) {
	var total Result
	merge := func(res Result) {
		total.FilesRemoved += res.FilesRemoved
		total.DirsRemoved += res.DirsRemoved
		total.BytesRemoved += res.BytesRemoved
		total.Errors += res.Errors
	}
	cleanBase := func(base string) {
		for _, rel := range disposableDirPaths {
			merge(removePath(filepath.Join(base, filepath.FromSlash(rel))))
		}
		for _, rel := range disposableFilePaths {
			merge(removePath(filepath.Join(base, filepath.FromSlash(rel))))
		}
	}

	// Chromium may keep cache both at the user-data root and inside Default /
	// Profile N. Enumerate only this first level, then address allow-listed paths
	// directly. Never recursively scan wallet, origin or extension storage just
	// to discover cache candidates.
	cleanBase(profileRoot)
	entries, err := os.ReadDir(profileRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return total, nil
		}
		return total, err
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && isChromiumProfileDir(entry.Name()) {
			cleanBase(filepath.Join(profileRoot, entry.Name()))
		}
	}
	return total, nil
}

func removePath(path string) Result {
	var res Result
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			res.Errors++
		}
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
		if entryInfo, err := d.Info(); err == nil {
			res.BytesRemoved += entryInfo.Size()
		}
		return nil
	})
	if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
		res.Errors++
	}
	return res
}
