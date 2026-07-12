//go:build windows

package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

const syncBridgeAddress = "127.0.0.1:47823"

type syncBridgeRequest struct {
	MasterID    string   `json:"masterId,omitempty"`
	FollowerIDs []string `json:"followerIds,omitempty"`
	ProfileIDs  []string `json:"profileIds,omitempty"`
	Layout      string   `json:"layout,omitempty"`
	Mouse       bool     `json:"mouse,omitempty"`
	MinMs       int      `json:"minMs,omitempty"`
	MaxMs       int      `json:"maxMs,omitempty"`
	Enabled     bool     `json:"enabled,omitempty"`
}

type syncBridgeEnvelope struct {
	OK       bool                   `json:"ok"`
	Error    string                 `json:"error,omitempty"`
	Profiles []SyncProfileInfo      `json:"profiles,omitempty"`
	Status   map[string]interface{} `json:"status,omitempty"`
	Tile     *TileWindowsResult     `json:"tile,omitempty"`
}

func (a *App) startSyncBridge() {
	if a == nil || a.panelMode {
		return
	}
	listener, err := net.Listen("tcp", syncBridgeAddress)
	if err != nil {
		a.lifecycleLog("sync-bridge", "state=listen-failed", "error="+err.Error())
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { writeSyncBridgeJSON(w, syncBridgeEnvelope{OK: true}) })
	mux.HandleFunc("/profiles", func(w http.ResponseWriter, _ *http.Request) {
		writeSyncBridgeJSON(w, syncBridgeEnvelope{OK: true, Profiles: a.getSyncProfilesLocal()})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		writeSyncBridgeJSON(w, syncBridgeEnvelope{OK: true, Status: a.getSyncStatusLocal()})
	})
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		var req syncBridgeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		err := a.startInputSyncLocal(req.MasterID, req.FollowerIDs)
		writeSyncBridgeResult(w, err)
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, _ *http.Request) { writeSyncBridgeResult(w, a.stopInputSyncLocal()) })
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		var req syncBridgeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeSyncBridgeResult(w, a.updateSyncConfigLocal(req.Mouse))
	})
	mux.HandleFunc("/delay", func(w http.ResponseWriter, r *http.Request) {
		var req syncBridgeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeSyncBridgeResult(w, a.updateSyncRandomDelayLocal(req.Enabled, req.MinMs, req.MaxMs))
	})
	mux.HandleFunc("/tile", func(w http.ResponseWriter, r *http.Request) {
		var req syncBridgeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		result, err := a.syncTileWindowsLocal(req.ProfileIDs, req.MasterID, req.Layout)
		if err != nil {
			writeSyncBridgeResult(w, err)
			return
		}
		writeSyncBridgeJSON(w, syncBridgeEnvelope{OK: true, Tile: result})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = server.Serve(listener) }()
	a.lifecycleLog("sync-bridge", "state=started", "address="+syncBridgeAddress)
}

func writeSyncBridgeResult(w http.ResponseWriter, err error) {
	if err != nil {
		writeSyncBridgeJSON(w, syncBridgeEnvelope{OK: false, Error: err.Error()})
		return
	}
	writeSyncBridgeJSON(w, syncBridgeEnvelope{OK: true})
}

func writeSyncBridgeJSON(w http.ResponseWriter, value syncBridgeEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func callSyncBridge(path string, request any) (syncBridgeEnvelope, error) {
	body := bytes.NewReader(nil)
	if request != nil {
		raw, _ := json.Marshal(request)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://"+syncBridgeAddress+path, body)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return syncBridgeEnvelope{}, fmt.Errorf("主客户端同步服务不可用: %w", err)
	}
	defer resp.Body.Close()
	var envelope syncBridgeEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return envelope, err
	}
	if !envelope.OK {
		return envelope, fmt.Errorf("%s", envelope.Error)
	}
	return envelope, nil
}
