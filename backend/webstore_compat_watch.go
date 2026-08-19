package backend

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"boost-browser/backend/internal/logger"
)

// startWebStoreCompatWatch listens for page targets on a non-cloak environment
// and injects Web Store compatibility only when the target is actually a
// Chrome / Edge / Opera store URL.
//
// This replaces the v1.7.125 Target.setAutoAttach-on-every-page owner.
// Auto-attach connected CDP to about:blank, OAuth popups and every new tab:
// that made environment open slow and is a nodriver / tampering signal that
// breaks one-time authorizations (X "You weren't able to give access").
//
// Target.setDiscoverTargets reports created/changed targets without attaching.
// Injection (UA override + webstorePrivate) happens only after
// shouldInjectWebStoreCompat is true. The browser-level watch dies with the
// environment process.
func startWebStoreCompatWatch(debugPort int, profileId string) {
	if debugPort <= 0 {
		return
	}
	for {
		err := runWebStoreCompatWatchSession(debugPort, profileId)
		if err == nil {
			return
		}
		if !browserDebugAlive(debugPort) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

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
	discoverMsg := cdpMessage{
		Id:     1,
		Method: "Target.setDiscoverTargets",
		Params: map[string]any{
			"discover": true,
		},
	}
	if err := conn.WriteJSON(discoverMsg); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var discoverResp cdpResponse
	if err := conn.ReadJSON(&discoverResp); err != nil {
		return err
	}
	if discoverResp.Error != nil {
		return fmt.Errorf("Target.setDiscoverTargets 失败: %s", discoverResp.Error.Message)
	}

	injected := map[string]struct{}{}
	for {
		conn.SetReadDeadline(time.Time{})
		var evt struct {
			Method string `json:"method"`
			Params struct {
				TargetInfo struct {
					TargetID string `json:"targetId"`
					Type     string `json:"type"`
					URL      string `json:"url"`
				} `json:"targetInfo"`
				TargetID string `json:"targetId"`
			} `json:"params"`
		}
		if err := conn.ReadJSON(&evt); err != nil {
			return err
		}
		switch evt.Method {
		case "Target.targetDestroyed":
			if id := evt.Params.TargetID; id != "" {
				delete(injected, id)
			}
			continue
		case "Target.targetCreated", "Target.targetInfoChanged":
		default:
			continue
		}
		info := evt.Params.TargetInfo
		if info.Type != "page" || info.TargetID == "" {
			continue
		}
		if !shouldInjectWebStoreCompat(info.URL) {
			continue
		}
		if _, done := injected[info.TargetID]; done {
			continue
		}
		injected[info.TargetID] = struct{}{}
		log.Info("商店页发现，注入 Web Store 兼容",
			logger.F("profile_id", profileId),
			logger.F("target_id", info.TargetID),
			logger.F("url", info.URL),
			logger.F("debug_port", debugPort),
		)
		applyWebStoreCompatToPageTarget(debugPort, info.TargetID, profileId)
	}
}

// applyWebStoreCompatToPageTarget injects Web Store compatibility on one
// already-confirmed store page:
//   - Emulation.setUserAgentOverride (real kernel UA-CH brand)
//   - Page.addScriptToEvaluateOnNewDocument (chrome.webstorePrivate)
//
// Callers must have filtered with shouldInjectWebStoreCompat. The page
// WebSocket is opened only for that store target and closed immediately.
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

	log.Info("已为商店页注入 Web Store 兼容（UA-CH 真实版本 + webstorePrivate，无 helper 协议）",
		logger.F("profile_id", profileId),
		logger.F("target_id", targetID),
		logger.F("debug_port", debugPort),
	)
}

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
