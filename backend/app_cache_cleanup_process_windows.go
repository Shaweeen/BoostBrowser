//go:build windows

package backend

func (a *App) discoverLiveCacheProfileRoots() (map[string]struct{}, error) {
	processes, err := discoverBoostBrowserProcesses(a.appRoot)
	if err != nil {
		return nil, err
	}
	live := make(map[string]struct{}, len(processes))
	for _, process := range processes {
		if key := normalizeCacheProfileRoot(process.UserDataDir); key != "" {
			live[key] = struct{}{}
		}
	}
	return live, nil
}
