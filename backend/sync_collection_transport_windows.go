//go:build windows

package backend

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	winio "github.com/Microsoft/go-winio"
)

const syncCollectionPipe = `\\.\pipe\BrowserStudio_SyncCollection_v2`

type syncCollectionRequest struct {
	Token   string `json:"token"`
	Refresh bool   `json:"refresh"`
}
type syncCollectionResponse struct {
	Snapshot SyncSnapshot `json:"snapshot"`
	Error    string       `json:"error,omitempty"`
}

func newSyncCollectionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// PrepareSyncPanelHandoff starts one owner-only local pipe and collects the
// opening batch in the main process before the panel process is launched.
func (a *App) PrepareSyncPanelHandoff() (string, error) {
	if a == nil || a.panelMode {
		return "", fmt.Errorf("同步数据只能由主客户端发布")
	}
	a.syncHandoffMu.Lock()
	defer a.syncHandoffMu.Unlock()
	if a.syncHandoffToken == "" {
		token, err := newSyncCollectionToken()
		if err != nil {
			return "", err
		}
		listener, err := winio.ListenPipe(syncCollectionPipe, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;OW)"})
		if err != nil {
			return "", err
		}
		a.syncHandoffToken = token
		stop := make(chan struct{})
		a.syncHandoffStop = func() { close(stop); _ = listener.Close() }
		go a.serveSyncCollection(listener, token, stop)
	}
	a.collectSyncSnapshotForPanel(false)
	return a.syncHandoffToken, nil
}

func (a *App) serveSyncCollection(listener net.Listener, token string, stop <-chan struct{}) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case <-stop:
			_ = conn.Close()
			return
		default:
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
			var req syncCollectionRequest
			if json.NewDecoder(bufio.NewReader(conn)).Decode(&req) != nil || req.Token == "" || req.Token != token {
				_ = json.NewEncoder(conn).Encode(syncCollectionResponse{Error: "未授权的同步数据请求"})
				return
			}
			var snapshot SyncSnapshot
			if req.Refresh {
				snapshot = a.collectSyncSnapshotForPanel(true)
			} else {
				generation, profiles := a.syncCollection.snapshot()
				snapshot = SyncSnapshot{Profiles: profiles, Status: a.getSyncStatusLocal(), Generation: generation}
			}
			_ = json.NewEncoder(conn).Encode(syncCollectionResponse{Snapshot: snapshot})
		}()
	}
}

func (a *App) requestMainSyncCollection(refresh bool) SyncSnapshot {
	if a.syncClientToken == "" {
		return SyncSnapshot{Profiles: []SyncProfileInfo{}, Status: map[string]interface{}{"error": "缺少主客户端同步会话"}}
	}
	timeout := 5 * time.Second
	conn, err := winio.DialPipe(syncCollectionPipe, &timeout)
	if err != nil {
		return SyncSnapshot{Profiles: []SyncProfileInfo{}, Status: map[string]interface{}{"error": "主客户端同步数据不可用"}}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	if json.NewEncoder(conn).Encode(syncCollectionRequest{Token: a.syncClientToken, Refresh: refresh}) != nil {
		return SyncSnapshot{Profiles: []SyncProfileInfo{}}
	}
	var response syncCollectionResponse
	if json.NewDecoder(bufio.NewReader(conn)).Decode(&response) != nil || response.Error != "" {
		return SyncSnapshot{Profiles: []SyncProfileInfo{}, Status: map[string]interface{}{"error": response.Error}}
	}
	a.syncCollection.replace(response.Snapshot.Profiles)
	a.applyMainSyncProfiles(response.Snapshot.Profiles)
	// Collection data belongs to the main client, while active/paused/config
	// state belongs to this panel process. Never overwrite panel sync state with
	// the main client's inactive status.
	response.Snapshot.Status = a.getSyncStatusLocal()
	return response.Snapshot
}

func (a *App) applyMainSyncProfiles(profiles []SyncProfileInfo) {
	if a.browserMgr == nil {
		return
	}
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	for _, p := range a.browserMgr.Profiles {
		if p == nil {
			continue
		}
		p.Running = false
		p.Pid = 0
		p.DebugPort = 0
		p.DebugReady = false
	}
	for _, info := range profiles {
		p := a.browserMgr.Profiles[info.ProfileId]
		if p == nil {
			p = &BrowserProfile{ProfileId: info.ProfileId, ProfileName: info.ProfileName}
			a.browserMgr.Profiles[info.ProfileId] = p
		}
		if strings.TrimSpace(info.ProfileName) != "" {
			p.ProfileName = info.ProfileName
		}
		p.Running, p.Pid, p.DebugPort, p.DebugReady = info.Running, info.Pid, info.DebugPort, info.DebugPort > 0
	}
}

func (a *App) StopSyncPanelHandoff() {
	a.syncHandoffMu.Lock()
	defer a.syncHandoffMu.Unlock()
	if a.syncHandoffStop != nil {
		a.syncHandoffStop()
		a.syncHandoffStop = nil
	}
	a.syncHandoffToken = ""
}
