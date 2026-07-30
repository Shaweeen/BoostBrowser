package backend

import (
	"boost-browser/backend/internal/logger"
	"fmt"
)

func (a *App) stopRuntimeServices() {
	a.stopServicesOnce.Do(func() {
		if a.speedScheduler != nil {
			a.speedScheduler.Stop()
			a.speedScheduler = nil
		}

		// Stop multi-open popup confinement before environments close so it does
		// not enumerate disappearing HWNDs during shutdown.
		a.unregisterEnvironmentPopupConfiner()

		// 跟随上游 Ant-Browser：退出时按顺序停止浏览器实例，避免并发 taskkill
		// 和残留进程扫描把 Wails 主进程/子进程状态打乱，造成主程序闪退或 watchdog 重启。
		a.stopTrackedBrowserProcesses()

		if a.xrayMgr != nil {
			a.xrayMgr.StopAll()
		}
		a.clearProfileXrayBridges()

		if a.clashMgr != nil {
			a.clashMgr.StopAll()
		}
		if a.singboxMgr != nil {
			a.singboxMgr.StopAll()
		}
		if a.standardRelayMgr != nil {
			a.standardRelayMgr.StopAll()
		}
	})
}

func (a *App) stopTrackedBrowserProcesses() {
	if a.browserMgr == nil {
		return
	}

	a.browserMgr.Mutex.Lock()
	profileIDs := make([]string, 0, len(a.browserMgr.Profiles))
	for profileID, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}
		cmd := a.browserMgr.BrowserProcesses[profileID]
		if profile.Running || cmd != nil {
			profileIDs = append(profileIDs, profileID)
		}
	}
	a.browserMgr.Mutex.Unlock()

	// Keep shutdown sequential and use the same per-environment write barrier as
	// an explicit Stop action. A failed graceful close remains visible instead of
	// silently force-killing an extension database during client shutdown.
	for _, profileID := range profileIDs {
		if _, err := a.BrowserInstanceStop(profileID); err != nil {
			logger.New("Browser").Error("退出客户端时环境未通过写盘关闭确认",
				logger.F("profile_id", profileID),
				logger.F("error", err.Error()),
			)
		}
	}
}

func (a *App) finalizeShutdown() {
	a.finalizeOnce.Do(func() {
		// 跟随上游 Ant-Browser 的生命周期：运行期服务/浏览器先停，数据库随后关闭，
		// launch server 最后关闭，避免 shutdown 过程中仍有 relay/state goroutine 访问已关闭服务。
		if a.db != nil {
			a.db.Close()
			a.db = nil
		}
		if a.launchServer != nil {
			_ = a.launchServer.Stop()
			a.launchServer = nil
		}
		if err := logger.Close(); err != nil {
			fmt.Printf("关闭日志系统失败: %v\n", err)
		}
	})
}
