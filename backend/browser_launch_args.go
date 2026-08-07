package backend

import (
	"boost-browser/backend/internal/logger"
	"strings"
)

type managedLaunchArgSpec struct {
	prefix     string
	takesValue bool
}

const (
	chromeTestingInfobarSuppressArg  = "--test-type"
	chromeTestingDisableInfobarsArg  = "--disable-infobars"
	defaultSearchProviderNameArg     = "--search-provider-name=Google"
	defaultSearchProviderKeywordArg  = "--search-provider-keyword=9oo91e.qjz9zk"
	defaultSearchProviderSearchArg   = "--search-provider-search-url=https://www.9oo91e.qjz9zk/search?q={searchTerms}"
	defaultSearchProviderSuggestArg  = "--search-provider-suggest-url=https://www.9oo91e.qjz9zk/complete/search?client=chrome&q={searchTerms}"
	defaultSearchProviderEncodingArg = "--search-provider-encodings=UTF-8"
)

var managedLaunchArgSpecs = []managedLaunchArgSpec{
	{prefix: "--user-data-dir", takesValue: true},
	{prefix: "--remote-debugging-port", takesValue: true},
	{prefix: "--remote-debugging-address", takesValue: true},
	{prefix: "--remote-debugging-pipe", takesValue: false},
	{prefix: "--proxy-server", takesValue: true},
	{prefix: "--user-agent", takesValue: true},
	{prefix: "--search-provider-name", takesValue: true},
	{prefix: "--search-provider-keyword", takesValue: true},
	{prefix: "--search-provider-search-url", takesValue: true},
	{prefix: "--search-provider-suggest-url", takesValue: true},
	{prefix: "--search-provider-encodings", takesValue: true},
}

var managedWindowPlacementArgSpecs = []managedLaunchArgSpec{
	{prefix: "--window-size", takesValue: true},
	{prefix: "--window-position", takesValue: true},
	{prefix: "--start-maximized", takesValue: false},
	{prefix: "--start-minimized", takesValue: false},
	{prefix: "--start-fullscreen", takesValue: false},
	{prefix: "--kiosk", takesValue: false},
}

func sanitizeManagedLaunchArgs(args []string) ([]string, []string) {
	return sanitizeLaunchArgsBySpecs(args, managedLaunchArgSpecs)
}

func sanitizeManagedWindowPlacementArgs(args []string) ([]string, []string) {
	return sanitizeLaunchArgsBySpecs(args, managedWindowPlacementArgSpecs)
}

func sanitizeLaunchArgsBySpecs(args []string, specs []managedLaunchArgSpec) ([]string, []string) {
	if len(args) == 0 {
		return nil, nil
	}

	sanitized := make([]string, 0, len(args))
	removed := make([]string, 0, 4)

	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}

		spec, matched := matchLaunchArgSpec(arg, specs)
		if !matched {
			sanitized = append(sanitized, arg)
			continue
		}

		removed = appendUniqueString(removed, spec.prefix)
		if spec.takesValue && !strings.Contains(arg, "=") && i+1 < len(args) {
			next := strings.TrimSpace(args[i+1])
			if next != "" && !strings.HasPrefix(next, "-") {
				i++
			}
		}
	}

	return sanitized, removed
}

func matchLaunchArgSpec(arg string, specs []managedLaunchArgSpec) (managedLaunchArgSpec, bool) {
	for _, spec := range specs {
		if strings.EqualFold(arg, spec.prefix) || strings.HasPrefix(strings.ToLower(arg), strings.ToLower(spec.prefix)+"=") {
			return spec, true
		}
	}
	return managedLaunchArgSpec{}, false
}

func logManagedLaunchArgOverrides(log *logger.Logger, profileId string, source string, managedArgs []string) {
	if log == nil || len(managedArgs) == 0 {
		return
	}
	log.Warn("忽略由系统接管的浏览器启动参数",
		logger.F("profile_id", profileId),
		logger.F("source", source),
		logger.F("managed_args", managedArgs),
	)
}

func appendUniqueString(items []string, value string) []string {
	for _, item := range items {
		if strings.EqualFold(item, value) {
			return items
		}
	}
	return append(items, value)
}

// appendChromeTestingInfobarSuppressArg 追加浏览器 infobar 抑制参数。
//
// --disable-infobars 两条路径都保留：压掉 "您使用的是不受支持的命令行标记:
// --no-sandbox" 黄色安全警告和 Chrome for Testing 自带的 non-closeable infobar。
//
// --test-type 只追加到非 cloak 路径。它是自动化/测试专用标志，X（Twitter）等
// 严格反自动化站点会把它与浏览器身份异常关联，触发「页面无法完整加载 / 登录
// 提醒 / 临时登录限制」。CloakBrowser 内核已在源码层消除 infobar，不再需要
// --test-type 的压制作用；去掉它可移除一个 Bot 检测信号（fingerprint.com 曾把
// --test-type 识别为 "Bot: google" 红灯），同时 --disable-infobars 仍足够压住
// --no-sandbox 黄条。
func appendChromeTestingInfobarSuppressArg(args []string, cloakOnly bool) []string {
	if !cloakOnly {
		args = appendLaunchArgIfMissing(args, chromeTestingInfobarSuppressArg)
	}
	args = appendLaunchArgIfMissing(args, chromeTestingDisableInfobarsArg)
	return args
}

func appendDefaultSearchProviderLaunchArgs(args []string, enabled bool) []string {
	if !enabled {
		return args
	}
	args = append(args,
		defaultSearchProviderNameArg,
		defaultSearchProviderKeywordArg,
		defaultSearchProviderSearchArg,
		defaultSearchProviderSuggestArg,
		defaultSearchProviderEncodingArg,
	)
	return args
}

func appendLaunchArgIfMissing(args []string, want string) []string {
	for _, arg := range args {
		if strings.EqualFold(launchArgKey(arg), want) {
			return args
		}
	}
	return append(args, want)
}

// preparePrimaryEnvironmentLaunchArgs keeps startup presentation policy in one
// place. The selected browser core owns its natural initial blank page.
// BrowserStudio removes positional startup targets left by older builds and
// never creates another default tab. Explicit product start URLs are opened
// later through CDP.
func preparePrimaryEnvironmentLaunchArgs(args []string) []string {
	out := make([]string, 0, len(args)+1)
	for _, arg := range args {
		if isBrowserStartupTargetArg(arg) {
			continue
		}
		out = append(out, arg)
	}
	return appendLaunchArgIfMissing(out, "--start-minimized")
}

func isBrowserStartupTargetArg(arg string) bool {
	value := strings.ToLower(strings.TrimSpace(arg))
	for _, prefix := range []string{
		"about:",
		"chrome:",
		"chrome-extension:",
		"http:",
		"https:",
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
