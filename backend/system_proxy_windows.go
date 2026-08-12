//go:build windows

package backend

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// readWindowsSystemProxy reads the current user's WinINet proxy without
// changing it. Clash Verge system-proxy mode writes this registry value.
func readWindowsSystemProxy() string {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return ""
	}
	raw, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, ";") || strings.Contains(raw, "=") {
		for _, item := range strings.Split(raw, ";") {
			parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
			if len(parts) == 2 && (strings.EqualFold(parts[0], "https") || strings.EqualFold(parts[0], "http")) {
				raw = strings.TrimSpace(parts[1])
				if strings.EqualFold(parts[0], "https") {
					break
				}
			}
		}
	}
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return raw
}
