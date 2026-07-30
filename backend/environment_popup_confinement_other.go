//go:build !windows

package backend

import "sync/atomic"

// environmentPopupApp is unused on non-Windows builds but kept so shared call
// sites compile without platform branches.
var environmentPopupApp atomic.Pointer[App]

type environmentPopupConfiner struct{}

func (a *App) registerEnvironmentPopupConfiner() {}

func (a *App) unregisterEnvironmentPopupConfiner() {}

func (a *App) holdEnvironmentPopupConfinement(hold bool) {}

func (a *App) scheduleEnvironmentPopupConfinementRefresh() {}

func (a *App) refreshEnvironmentPopupConfinement() {}
