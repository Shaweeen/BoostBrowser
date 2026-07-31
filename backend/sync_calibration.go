package backend

import (
	"math"
	"strings"
	"unicode/utf8"
)

// Chrome-Manager calibration primitives.
// Source of truth: https://github.com/galaylm/Chrome-Manager-Clean (chrome_manager.py)
//
// Algorithms mirrored from that sync assistant:
//  1. Map pointer with OUTER window rect (GetWindowRect): same-size → absolute 1:1,
//     different size → proportional rel_x/rel_y (on_mouse_event).
//  2. Floating wallet popups: match by size_delta * (2 - title_jaccard) primarily.
//  3. Anchored popups: relative offset to parent (popup - main) on follower.
//  4. Throttle mouse moves by time AND pixel distance (move_interval + mouse_threshold).

// sameSizePixelTolerance: uniform tile cells (and same-size stack) should map
// 1:1 absolute so scrollbars / IME carets / hit targets stay pixel-true.
const sameSizePixelTolerance = 4

// chromeManagerMapPoint maps a screen point from a master surface outer rect
// into client coordinates of a follower surface of size (fW,fH).
// When master and follower sizes match (within tolerance), use absolute pixel
// offsets — Chrome-Manager proportional only when sizes differ.
func chromeManagerMapPoint(screenX, screenY, mLeft, mTop, mRight, mBottom, fW, fH int) (clientX, clientY int, ok bool) {
	mW := mRight - mLeft
	mH := mBottom - mTop
	if mW <= 0 || mH <= 0 || fW <= 0 || fH <= 0 {
		return 0, 0, false
	}
	// Allow a few pixels of DPI/edge slack (CM only checks strict inside).
	if screenX < mLeft-2 || screenX > mRight+2 || screenY < mTop-2 || screenY > mBottom+2 {
		return 0, 0, false
	}

	// Same-size windows: absolute client offset (master origin → follower).
	// Proportional rounding was the main source of scrollbar/dapp click drift
	// under uniform tile grids.
	if absCalibInt(mW-fW) <= sameSizePixelTolerance && absCalibInt(mH-fH) <= sameSizePixelTolerance {
		clientX = screenX - mLeft
		clientY = screenY - mTop
	} else {
		relX := float64(screenX-mLeft) / float64(mW)
		relY := float64(screenY-mTop) / float64(mH)
		if relX < 0 {
			relX = 0
		}
		if relX > 1 {
			relX = 1
		}
		if relY < 0 {
			relY = 0
		}
		if relY > 1 {
			relY = 1
		}
		clientX = int(math.Round(float64(fW) * relX))
		clientY = int(math.Round(float64(fH) * relY))
	}
	if clientX < 0 {
		clientX = 0
	}
	if clientY < 0 {
		clientY = 0
	}
	if clientX > fW {
		clientX = fW
	}
	if clientY > fH {
		clientY = fH
	}
	if clientX > 32767 || clientY > 32767 {
		return 0, 0, false
	}
	return clientX, clientY, true
}

// titleSimilarityJaccard is Chrome-Manager's title_similarity (character-set Jaccard).
// Returns 0..1 where 1 is identical (empty/empty => 1).
func titleSimilarityJaccard(title1, title2 string) float64 {
	a := strings.ToLower(strings.TrimSpace(title1))
	b := strings.ToLower(strings.TrimSpace(title2))
	if a == "" && b == "" {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	setA := make(map[rune]struct{}, utf8.RuneCountInString(a))
	setB := make(map[rune]struct{}, utf8.RuneCountInString(b))
	for _, r := range a {
		setA[r] = struct{}{}
	}
	for _, r := range b {
		setB[r] = struct{}{}
	}
	inter := 0
	for r := range setA {
		if _, ok := setB[r]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union <= 0 {
		return 1
	}
	return float64(inter) / float64(union)
}

func absCalibInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// chromeManagerPopupMatchScore ranks a follower popup candidate (lower is better).
// Combines size delta, relative-offset delta, and title dissimilarity — the same
// three signals Chrome-Manager uses in on_mouse_event / sync_specific_popup.
func chromeManagerPopupMatchScore(
	masterW, masterH int,
	candidateW, candidateH int,
	expectedLeft, expectedTop int,
	candidateLeft, candidateTop int,
	masterTitle, candidateTitle string,
) int64 {
	sizeDelta := absCalibInt(masterW-candidateW) + absCalibInt(masterH-candidateH)
	positionDelta := absCalibInt(expectedLeft-candidateLeft) + absCalibInt(expectedTop-candidateTop)
	titleSim := titleSimilarityJaccard(masterTitle, candidateTitle)
	// CM floating path: size_diff * (2.0 - title_sim) dominates for WS_POPUP wallets.
	if isChromeManagerFloatingPopupSize(masterW, masterH) {
		return int64(math.Round(float64(sizeDelta)*(2.0-titleSim)*1000.0)) + int64(positionDelta)/4
	}
	// Anchored / secondary chrome: relative offset to parent is the primary key.
	titlePenalty := int64(math.Round((1.0 - titleSim) * 5000))
	return int64(sizeDelta)*1000 + int64(positionDelta) + titlePenalty
}

// isChromeManagerFloatingPopupSize mirrors CM wallet_size / floating_layer bounds
// (compact surfaces, not full browser frames).
func isChromeManagerFloatingPopupSize(w, h int) bool {
	return w >= 120 && h >= 80 && w <= 800 && h <= 800
}

// mouseMovePixelThreshold is Chrome-Manager's mouse_threshold (2px) — ignore
// micro-jitter so multi-follower dispatch stays smooth.
const mouseMovePixelThreshold = 2

// shouldThrottleMouseMoveByDistance returns true when the pointer has not moved
// far enough since the last delivered sample (CM distance throttle).
func shouldThrottleMouseMoveByDistance(lastX, lastY, x, y, threshold int) bool {
	if threshold <= 0 {
		return false
	}
	dx := absCalibInt(x - lastX)
	dy := absCalibInt(y - lastY)
	return dx < threshold && dy < threshold
}

// expectedPopupOffsetLeft mirrors Chrome-Manager's relative_x placement:
// popup.left - master.left mapped onto the follower origin.
func expectedPopupOffsetLeft(masterPopupLeft, masterMainLeft, followerMainLeft int) int {
	return followerMainLeft + (masterPopupLeft - masterMainLeft)
}

func expectedPopupOffsetTop(masterPopupTop, masterMainTop, followerMainTop int) int {
	return followerMainTop + (masterPopupTop - masterMainTop)
}
