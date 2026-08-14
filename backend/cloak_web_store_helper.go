package backend

import (
	"strings"

	"boost-browser/backend/internal/logger"
)

// cloakWebStoreHelperForLaunch has one owner and no polling: the first Cloak
// environment start materializes the embedded helper, later starts only reuse
// it. The helper is not attached to branded Chrome and it is not written into
// any user's saved extension assignment list.
func (a *App) cloakWebStoreHelperForLaunch() string {
	if a == nil {
		return ""
	}
	a.cloakStoreOnce.Do(func() {
		a.cloakStoreHelperDir, a.cloakStoreHelperErr = ensureEmbeddedCloakExtensions(a.appRoot)
		if a.cloakStoreHelperErr != nil {
			logger.New("Extension").Warn("Cloak 扩展商城助手准备失败；已不影响用户已分配扩展",
				logger.F("error", a.cloakStoreHelperErr.Error()),
			)
			return
		}
		if a.launchServer != nil && a.launchServer.Port() > 0 && a.config != nil {
			if err := writeHelperBoostEndpoint(a.appRoot, a.launchServer.Port(), a.config.LaunchServer.Auth.Header, a.config.LaunchServer.Auth.APIKey); err != nil {
				logger.New("Extension").Warn("写入 Cloak 扩展商城助手本地端点失败",
					logger.F("error", err.Error()),
				)
			}
		}
	})
	if a.cloakStoreHelperErr != nil || strings.TrimSpace(a.cloakStoreHelperDir) == "" {
		return ""
	}
	return a.cloakStoreHelperDir
}
