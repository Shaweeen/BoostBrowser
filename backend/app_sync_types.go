package backend

// SyncProfileInfo 同步页面的实例信息
type SyncProfileInfo struct {
	ProfileId   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	Pid         int    `json:"pid"`
	DebugPort   int    `json:"debugPort"`
	Hwnd        int64  `json:"hwnd"`
	Running     bool   `json:"running"`
	Status      string `json:"status"` // "running" | "no_window" | "stopped"
	BadgeNumber int    `json:"badgeNumber"`
}

// SyncSnapshot returns profiles and session state from one serialized boundary.
// The UI must not combine a newly scanned window list with status from an older
// Start/Stop generation.
type SyncSnapshot struct {
	Profiles   []SyncProfileInfo      `json:"profiles"`
	Status     map[string]interface{} `json:"status"`
	Generation uint64                 `json:"generation"`
}
