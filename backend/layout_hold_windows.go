//go:build windows

package backend

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// layoutHoldRoot is used only by the explicit tile/stack operation to avoid
// dispatching input against a half-moved main browser window. It does not own,
// enumerate, resize or continuously observe extension popup windows.
var layoutHoldRoot atomic.Value // string

func setLayoutHoldRoot(appRoot string) {
	appRoot = strings.TrimSpace(appRoot)
	if appRoot != "" {
		layoutHoldRoot.Store(appRoot)
	}
}

func layoutHoldFlagPath() string {
	root, _ := layoutHoldRoot.Load().(string)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "data", "layout-hold.flag")
}

func setSharedLayoutHold(hold bool) {
	path := layoutHoldFlagPath()
	if path == "" {
		return
	}
	if hold {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
		return
	}
	_ = os.Remove(path)
}

func sharedLayoutHoldActive() bool {
	path := layoutHoldFlagPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	return err != nil || time.Since(t) < 3*time.Second
}
