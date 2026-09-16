package backend

import "math"

type nativeMouseTarget struct {
	owner, window, point uintptr
	pid                  uint32
}

type nativeMousePress struct {
	generation uint64
	targets    []nativeMouseTarget
}

// Owned by the hook thread. Opening a popup can change WindowFromPoint between
// mouse-down and mouse-up; both edges must still go to the original control.
type nativeMouseSequence map[uint32]nativeMousePress

func (s nativeMouseSequence) resolve(button uint32, down bool, generation uint64, discover func() []nativeMouseTarget) []nativeMouseTarget {
	if down {
		targets := discover()
		s[button] = nativeMousePress{generation: generation, targets: targets}
		return targets
	}
	press, ok := s[button]
	delete(s, button)
	if !ok || press.generation != generation {
		return nil
	}
	return press.targets
}

// mapChromeToolbarPoint maps native browser chrome, whose controls keep their
// DPI-scaled size when a window is tiled. In particular, changing the height of
// the page must never move an extension click into the caption-button row.
// Inputs and outputs are CLIENT coordinates, not outer-window offsets.
func mapChromeToolbarPoint(x, y, masterWidth, followerWidth, masterContentTop, followerContentTop, masterDPI, followerDPI int) (int, int, bool) {
	if masterWidth <= 0 || followerWidth <= 0 || masterDPI <= 0 || followerDPI <= 0 ||
		x < 0 || x >= masterWidth || y < 0 || y >= masterContentTop {
		return 0, 0, false
	}
	scale := float64(followerDPI) / float64(masterDPI)
	fx := int(math.Round(float64(x) * scale))
	// Navigation controls are left-anchored; extensions and the browser menu
	// are right-anchored. This assumes the same toolbar configuration in each
	// environment; it cannot identify differently pinned extensions by name.
	if x >= masterWidth/2 {
		fx = followerWidth - int(math.Round(float64(masterWidth-x)*scale))
	}
	fy := int(math.Round(float64(y) * scale))
	if fx < 0 || fx >= followerWidth || fy < 0 || fy >= followerContentTop || fx > 32767 || fy > 32767 {
		return 0, 0, false
	}
	return fx, fy, true
}
