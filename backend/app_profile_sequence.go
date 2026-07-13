package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var profileNameSequenceMu sync.Mutex

type profileNameSequenceState struct {
	HighWater map[string]int `json:"highWater"`
}

func (a *App) reserveProfileNameRange(prefix string, requestedStart, count int) (int, error) {
	profileNameSequenceMu.Lock()
	defer profileNameSequenceMu.Unlock()

	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "实例"
	}
	if requestedStart < 1 {
		requestedStart = 1
	}
	if count < 1 {
		return 0, fmt.Errorf("批量创建数量必须大于0")
	}

	key := strings.ToLower(prefix)
	highWater := 0
	if a != nil && a.browserMgr != nil {
		for _, profile := range a.browserMgr.List() {
			name := strings.TrimSpace(profile.ProfileName)
			if len(name) <= len(prefix)+1 || !strings.EqualFold(name[:len(prefix)], prefix) || name[len(prefix)] != '-' {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSpace(name[len(prefix)+1:]))
			if err == nil && n > highWater {
				highWater = n
			}
		}
	}

	path := a.resolveAppPath(filepath.Join("data", "profile-name-sequences.json"))
	state := profileNameSequenceState{HighWater: make(map[string]int)}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.HighWater == nil {
		state.HighWater = make(map[string]int)
	}
	if state.HighWater[key] > highWater {
		highWater = state.HighWater[key]
	}

	start := requestedStart
	if start <= highWater {
		start = highWater + 1
	}
	state.HighWater[key] = start + count - 1

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("保存实例编号状态失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("创建实例编号目录失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return 0, fmt.Errorf("写入实例编号状态失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows does not replace an existing destination with Rename.
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(tmp)
			return 0, fmt.Errorf("替换实例编号状态失败: %w", removeErr)
		}
		if renameErr := os.Rename(tmp, path); renameErr != nil {
			_ = os.Remove(tmp)
			return 0, fmt.Errorf("提交实例编号状态失败: %w", renameErr)
		}
	}
	return start, nil
}
