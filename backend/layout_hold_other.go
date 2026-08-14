//go:build !windows

package backend

func setLayoutHoldRoot(appRoot string) {}
func setSharedLayoutHold(hold bool) {}
func sharedLayoutHoldActive() bool { return false }
