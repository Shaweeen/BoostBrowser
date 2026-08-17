package backend

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"boost-browser/backend/internal/logger"
)

// startWebStoreCompatWatch 在非 cloak 内核环境上监听内核创建的所有标签页
// （浏览器级 Target.setAutoAttach），对每个新 page target 注入 Web Store
// 兼容（真实内核版本的 UA-CH 品牌 + chrome.webstorePrivate 补齐）。这样无论
// 用户在新标签页、新窗口还是弹窗中打开 chromewebstore.google.com，都不会
// 再出现「改用 Chrome？」横幅。
//
// 为什么需要它：applyWebStoreCompatibilityToInitialTab 只覆盖启动时的初始
// 标签页；用户新建标签页、window.open 弹窗等新 target 不在覆盖范围内，
// 横幅会再次出现。Target.setAutoAttach 是 Playwright 等工具实现
// “上下文级 UA 覆写覆盖所有页面”的标准机制。
//
// 只作用于该环境自己的浏览器进程（debugPort），不引入 helper 扩展或任何
// 本地协议；浏览器进程退出后监听随之结束，不常驻内存。
func startWebStoreCompatWatch(debugPort int, profileId string) {
	if debugPort <= 0 {
		return
	}
	for {
		err := runWebStoreCompatWatchSession(debugPort, profileId)
		if err == nil {
			// 浏览器进程关闭，正常结束监听。
			return
		}
		if !browserDebugAlive(debugPort) {
			return
		}
		// 浏览器仍在但连接异常（例如应用重启竞态），稍后重连。
		time.Sleep(2 * time.Second)
	}
}

// runWebStoreCompatWatchSession 建立一次浏览器级 CDP 会话并进入事件循环，
// 直到连接断开。返回 nil 表示浏览器已关闭。
func runWebStoreCompatWatchSession(debugPort int, profileId string) error {
	log := logger.New("WebStoreCompat")
	browserWS, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.Dial(browserWS, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	attachMsg := cdpMessage{
		Id:     1,
		Method: "Target.setAutoAttach",
		Params: map[string]any{
			"autoAttach":             true,
			"waitForDebuggerOnStart": false,
			"flatten":                true,
		},
	}
	if err := conn.WriteJSON(attachMsg); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var attachResp cdpResponse
	if err := conn.ReadJSON(&attachResp); err != nil {
		return err
	}
	if attachResp.Error != nil {
		return fmt.Errorf("Target.setAutoAttach 失败: %s", attachResp.Error.Message)
	}

	// 事件循环：auto-attach 生效后，浏览器对现有与未来所有 target 发送
	// Target.attachedToTarget 事件（flatten 模式附带 sessionId）。只关心
	// page 类型的新标签页，其余事件（console/network 等）一律忽略。
	for {
		conn.SetReadDeadline(time.Time{})
		var evt struct {
			Method string `json:"method"`
			Params struct {
				TargetInfo struct {
					TargetID string `json:"targetId"`
					Type     string `json:"type"`
				} `json:"targetInfo"`
			} `json:"params"`
		}
		if err := conn.ReadJSON(&evt); err != nil {
			return err
		}
		if evt.Method != "Target.attachedToTarget" || evt.Params.TargetInfo.Type != "page" {
			continue
		}
		targetID := evt.Params.TargetInfo.TargetID
		if targetID == "" {
			continue
		}
		log.Info("新标签页自动附加，注入 Web Store 兼容",
			logger.F("profile_id", profileId),
			logger.F("target_id", targetID),
			logger.F("debug_port", debugPort),
		)
		applyWebStoreCompatToPageTarget(debugPort, targetID, profileId)
	}
}

// applyWebStoreCompatToPageTarget 对指定 page target 注入 Web Store 兼容：
//   - Emulation.setUserAgentOverride（真实内核版本的 UA-CH 品牌）→ 修复
//     Sec-CH-UA 请求头与 navigator.userAgentData，消除「改用 Chrome？」横幅
//   - Page.addScriptToEvaluateOnNewDocument（chrome.webstorePrivate 补齐）→
//     「添加至 Chrome」按钮可用，CRX 下载触发内核原生安装框
//
// 覆写在 target 级生效并随后续导航保留。注入后即关闭连接，不常驻。
// cloak 内核在 C++ 层已处理品牌呈现，调用方应跳过。
func applyWebStoreCompatToPageTarget(debugPort int, targetID, profileId string) {
	log := logger.New("Browser")
	if debugPort <= 0 || targetID == "" {
		return
	}
	pageWs := fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/%s", debugPort, targetID)
	conn, _, err := websocket.DefaultDialer.Dial(pageWs, nil)
	if err != nil {
		log.Warn("Web Store 兼容注入：连接标签页失败",
			logger.F("profile_id", profileId),
			logger.F("target_id", targetID),
			logger.F("error", err.Error()),
		)
		return
	}
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	fixedUA, metadata, uaErr := getUserAgentOverride(debugPort)
	if uaErr == nil && fixedUA != "" && metadata != nil {
		uaMsg := cdpMessage{
			Id:     1,
			Method: "Emulation.setUserAgentOverride",
			Params: map[string]any{
				"userAgent":         fixedUA,
				"platform":          "Win32",
				"userAgentMetadata": metadata,
			},
		}
		_ = conn.WriteJSON(uaMsg)
		var uaResp cdpResponse
		_ = conn.ReadJSON(&uaResp)
	}

	scriptMsg := cdpMessage{
		Id:     2,
		Method: "Page.addScriptToEvaluateOnNewDocument",
		Params: map[string]any{"source": webStoreCompatScript},
	}
	_ = conn.WriteJSON(scriptMsg)
	var scriptResp cdpResponse
	_ = conn.ReadJSON(&scriptResp)

	log.Info("已为标签页注入 Web Store 兼容（UA-CH 真实版本 + webstorePrivate，无 helper 协议）",
		logger.F("profile_id", profileId),
		logger.F("target_id", targetID),
		logger.F("debug_port", debugPort),
	)
}

// browserDebugAlive 探测指定调试端口上的浏览器进程是否仍在运行。
func browserDebugAlive(debugPort int) bool {
	if debugPort <= 0 {
		return false
	}
	client := http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", debugPort))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
