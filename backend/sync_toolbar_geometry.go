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
	fx := mapChromeToolbarX(x, masterWidth, followerWidth, scale)
	fy := int(math.Round(float64(y) * scale))
	if fx < 0 || fx >= followerWidth || fy < 0 || fy >= followerContentTop || fx > 32767 || fy > 32767 {
		return 0, 0, false
	}
	return fx, fy, true
}

// mapChromeToolbarX keeps browser chrome in three coordinate domains. Left and
// right controls keep their physical edge inset, while the central omnibox/tab
// region follows the actual selected follower width proportionally. The edge
// zones are derived from both windows' current widths and DPI, never a fixed
// resolution or follower count.
func mapChromeToolbarX(x, masterWidth, followerWidth int, dpiScale float64) int {
	if masterWidth <= 0 || followerWidth <= 0 || dpiScale <= 0 {
		return -1
	}
	leftInset := float64(x)
	rightInset := float64(masterWidth - x)
	// An edge region can consume at most one quarter of the master toolbar and
	// at most half of the corresponding follower toolbar. This keeps a narrow
	// follower's extension/menu cluster anchored without interpreting the
	// address bar's centre/right text area as an extension icon.
	edgeLimit := math.Min(float64(masterWidth)/4, float64(followerWidth)/(2*dpiScale))
	if leftInset <= edgeLimit {
		return int(math.Round(leftInset * dpiScale))
	}
	if rightInset <= edgeLimit {
		return followerWidth - int(math.Round(rightInset*dpiScale))
	}
	return int(math.Round(float64(x) * float64(followerWidth) / float64(masterWidth)))
}
