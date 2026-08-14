//go:build !windows

package backend

func (a *App) discoverLiveCacheProfileRoots() (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
