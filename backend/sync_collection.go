package backend

import (
	"strings"
	"sync"
)

const syncCollectionTokenArg = "--sync-collection-token="

func syncCollectionTokenFromArgs(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, syncCollectionTokenArg) {
			return strings.TrimSpace(strings.TrimPrefix(arg, syncCollectionTokenArg))
		}
	}
	return ""
}

type syncCollection struct {
	mu         sync.RWMutex
	generation uint64
	profiles   []SyncProfileInfo
}

func (c *syncCollection) replace(profiles []SyncProfileInfo) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.profiles = append([]SyncProfileInfo(nil), profiles...)
	return c.generation
}

func (c *syncCollection) snapshot() (uint64, []SyncProfileInfo) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.generation, append([]SyncProfileInfo(nil), c.profiles...)
}
