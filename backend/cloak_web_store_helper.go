package backend

import (
	"path/filepath"
	"strings"

	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/logger"
)

// browserCoreNeedsWebStoreHelper is deliberately limited to cores that do not
// provide Google Chrome's native Web Store installer. Official branded Chrome
// remains untouched; verified Chrome for Testing and Cloak need the bundled
// compatibility component.
func browserCoreNeedsWebStoreHelper(core browser.Core, chromeBinaryPath string) bool {
	if isCloakCore(core, chromeBinaryPath) {
		return true
	}
	return isChromeForTestingKernelDir(filepath.Dir(chromeBinaryPath))
}

func appendWebStoreHelperLaunchArgs(args []string, helperDir string) []string {
	if strings.TrimSpace(helperDir) == "" {
		return args
	}
	return ensureLoadExtensionCommandLineSwitchEnabled(addExtensionDirToLaunchArgs(args, helperDir))
}

// webStoreHelperForProfileLaunch materializes one small system component under
// this UUID environment. It never writes Chromium-owned files and never adds an
// extension-center assignment. A per-profile copy carries the exact profileId,
// so five simultaneously open environments cannot install into one another.
func (a *App) webStoreHelperForProfileLaunch(profileID, userDataDir string) string {
	if a == nil || a.launchServer == nil || a.config == nil {
		return ""
	}
	helperDir, err := ensureEmbeddedWebStoreHelper(userDataDir)
	if err != nil {
		logger.New("Extension").Warn("扩展商城兼容组件准备失败；不影响已有用户扩展",
			logger.F("profile_id", profileID),
			logger.F("error", err.Error()),
		)
		return ""
	}
	if err := writeHelperBoostEndpoint(
		helperDir,
		a.launchServer.Port(),
		a.config.LaunchServer.Auth.Header,
		a.config.LaunchServer.Auth.APIKey,
		profileID,
	); err != nil {
		logger.New("Extension").Warn("写入扩展商城兼容组件本地端点失败",
			logger.F("profile_id", profileID),
			logger.F("error", err.Error()),
		)
	}
	return helperDir
}
