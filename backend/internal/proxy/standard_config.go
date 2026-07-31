package proxy

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// NormalizeStandardProxyConfig converts common provider text formats into one
// canonical URL. It intentionally does not guess between HTTP and SOCKS5; the
// selected/default protocol is used when the source omits one.
//
// Supported examples:
//   - host:port
//   - host:port:username:password
//   - host port username password
//   - host|port|username|password
//   - username:password@host:port
//   - http://username:password@host:port
func NormalizeStandardProxyConfig(raw, defaultScheme string) (string, error) {
	input := strings.TrimSpace(strings.Trim(raw, `"'`))
	if input == "" {
		return "", fmt.Errorf("代理内容为空")
	}

	defaultScheme = normalizeStandardProxyScheme(defaultScheme)
	if defaultScheme == "" {
		defaultScheme = "http"
	}

	scheme := ""
	remainder := input
	if idx := strings.Index(input, "://"); idx > 0 {
		scheme = normalizeStandardProxyScheme(input[:idx])
		if scheme == "" {
			return "", fmt.Errorf("不支持的代理协议 %q", input[:idx])
		}
		remainder = strings.TrimSpace(input[idx+3:])
	}

	if scheme == "" {
		fields := splitLooseProxyFields(remainder)
		if len(fields) > 0 {
			if fieldScheme := normalizeStandardProxyScheme(fields[0]); fieldScheme != "" && len(fields) >= 3 {
				scheme = fieldScheme
				fields = fields[1:]
				remainder = strings.Join(fields, " ")
			}
		}
	}
	if scheme == "" {
		scheme = defaultScheme
	}

	fields := splitLooseProxyFields(remainder)
	if len(fields) >= 2 && validProxyPort(fields[1]) {
		password := ""
		if len(fields) > 3 {
			password = strings.Join(fields[3:], " ")
		}
		return buildStandardProxyURL(scheme, fields[0], fields[1], valueAt(fields, 2), password)
	}

	// Colon form keeps every colon after the username as part of the password.
	// Bracketed IPv6 without credentials is handled by net/url below.
	//
	// Two common provider layouts:
	//   host:port:user:pass     (port is field 1; password may contain '@')
	//   user:pass:host:port     (exactly 4 fields; no '@' — avoids IPv6 auth URLs)
	if !strings.HasPrefix(remainder, "[") {
		parts := strings.Split(remainder, ":")
		if len(parts) >= 2 && validProxyPort(parts[1]) {
			password := ""
			if len(parts) > 3 {
				password = strings.Join(parts[3:], ":")
			}
			return buildStandardProxyURL(scheme, parts[0], parts[1], valueAt(parts, 2), password)
		}
		if !strings.Contains(remainder, "@") && len(parts) == 4 {
			if validProxyPort(parts[3]) && !validProxyPort(parts[1]) && parts[2] != "" {
				return buildStandardProxyURL(scheme, parts[2], parts[3], parts[0], parts[1])
			}
		}
	}

	// Standard user:password@host:port works best through net/url and also
	// preserves passwords containing ':'.
	if strings.Contains(remainder, "@") {
		return normalizeStandardProxyURL(scheme + "://" + remainder)
	}

	return normalizeStandardProxyURL(scheme + "://" + remainder)
}

// LooksLikeStandardProxyConfig distinguishes direct HTTP/SOCKS provider text
// from Clash YAML and URI-based tunnel protocols before normalizing storage.
func LooksLikeStandardProxyConfig(raw string) bool {
	input := strings.TrimSpace(strings.Trim(raw, `"'`))
	if input == "" {
		return false
	}
	lower := strings.ToLower(input)
	for _, prefix := range []string{"http://", "https://", "socks://", "socks5://", "socks5h://", "socket://"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if strings.Contains(input, "://") || strings.Contains(input, "\n") {
		return false
	}
	fields := splitLooseProxyFields(input)
	if len(fields) >= 2 && validProxyPort(fields[1]) {
		return true
	}
	if strings.Contains(input, "@") {
		at := strings.LastIndex(input, "@")
		hostPort := input[at+1:]
		if idx := strings.LastIndex(hostPort, ":"); idx > 0 && validProxyPort(hostPort[idx+1:]) {
			return true
		}
	}
	if !strings.HasPrefix(input, "[") && !strings.Contains(input, "@") {
		parts := strings.Split(input, ":")
		if len(parts) >= 2 && validProxyPort(parts[1]) {
			return true
		}
		// user:pass:host:port
		if len(parts) == 4 && validProxyPort(parts[3]) && !validProxyPort(parts[1]) {
			return true
		}
	}
	return false
}

func normalizeStandardProxyScheme(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "http":
		return "http"
	case "https":
		return "https"
	// socks5h = SOCKS5 with remote DNS (Firefox naming). Chromium has no
	// separate socks5h flag; we normalize to socks5 and always dial hostnames
	// through the local relay so DNS leaves via the proxy (same end effect).
	case "socks", "socks5", "socks5h", "socket":
		return "socks5"
	default:
		return ""
	}
}

// PreferredSchemeFromProxySource picks a normalize default from explicit labels.
// Bare host:port lines still default to the caller's preference (usually http).
func PreferredSchemeFromProxySource(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(lower, "socks5h://"),
		strings.HasPrefix(lower, "socks5://"),
		strings.HasPrefix(lower, "socks://"),
		strings.HasPrefix(lower, "socket://"):
		return "socks5"
	case strings.HasPrefix(lower, "https://"):
		return "https"
	case strings.HasPrefix(lower, "http://"):
		return "http"
	default:
		fields := splitLooseProxyFields(raw)
		if len(fields) > 0 {
			if s := normalizeStandardProxyScheme(fields[0]); s != "" {
				return s
			}
		}
		return ""
	}
}

func splitLooseProxyFields(raw string) []string {
	return strings.FieldsFunc(strings.TrimSpace(raw), func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '|' || r == ';'
	})
}

func valueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}

func validProxyPort(raw string) bool {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	return err == nil && port >= 1 && port <= 65535
}

func buildStandardProxyURL(scheme, rawHost, rawPort, username, password string) (string, error) {
	host := strings.Trim(strings.TrimSpace(rawHost), "[]")
	if host == "" || strings.ContainsFunc(host, unicode.IsSpace) {
		return "", fmt.Errorf("代理地址无效")
	}
	if !validProxyPort(rawPort) {
		return "", fmt.Errorf("代理端口必须在 1-65535 之间")
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strings.TrimSpace(rawPort)),
	}
	username = strings.TrimSpace(username)
	if username != "" {
		if password != "" {
			u.User = url.UserPassword(username, password)
		} else {
			u.User = url.User(username)
		}
	} else if password != "" {
		return "", fmt.Errorf("填写密码时必须同时填写账号")
	}
	return u.String(), nil
}

func normalizeStandardProxyURL(raw string) (string, error) {
	input := strings.TrimSpace(raw)
	schemeEnd := strings.Index(input, "://")
	if schemeEnd <= 0 {
		return "", fmt.Errorf("代理格式无效")
	}
	scheme := normalizeStandardProxyScheme(input[:schemeEnd])
	if scheme == "" {
		return "", fmt.Errorf("不支持的代理协议 %q", input[:schemeEnd])
	}
	remainder := input[schemeEnd+3:]

	// Parse from the last '@' so unescaped '@', '#', ':' and '/' in provider
	// passwords are encoded instead of being mistaken for URL syntax.
	if at := strings.LastIndex(remainder, "@"); at > 0 {
		userInfo := remainder[:at]
		endpoint := remainder[at+1:]
		endpointURL, err := url.Parse(scheme + "://" + endpoint)
		if err != nil || endpointURL.Hostname() == "" {
			return "", fmt.Errorf("代理地址无效")
		}
		username, password, hasPassword := strings.Cut(userInfo, ":")
		if decoded, decodeErr := url.PathUnescape(username); decodeErr == nil {
			username = decoded
		}
		if hasPassword {
			if decoded, decodeErr := url.PathUnescape(password); decodeErr == nil {
				password = decoded
			}
		}
		if !hasPassword {
			password = ""
		}
		return buildStandardProxyURL(scheme, endpointURL.Hostname(), endpointURL.Port(), username, password)
	}

	u, err := url.Parse(input)
	if err != nil {
		return "", fmt.Errorf("代理格式无效: %w", err)
	}
	host := strings.TrimSpace(u.Hostname())
	port := strings.TrimSpace(u.Port())
	if host == "" {
		return "", fmt.Errorf("缺少代理地址")
	}
	if !validProxyPort(port) {
		return "", fmt.Errorf("代理端口必须在 1-65535 之间")
	}

	username := ""
	password := ""
	hasPassword := false
	if u.User != nil {
		username = u.User.Username()
		password, hasPassword = u.User.Password()
	}
	normalized := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	if username != "" {
		if hasPassword {
			normalized.User = url.UserPassword(username, password)
		} else {
			normalized.User = url.User(username)
		}
	} else if hasPassword && password != "" {
		return "", fmt.Errorf("填写密码时必须同时填写账号")
	}
	return normalized.String(), nil
}
