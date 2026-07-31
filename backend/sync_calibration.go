package backend

import (
	"math"
	"strings"
	"unicode/utf8"
)

// Chrome-Manager calibration primitives (ported from galaylm/Chrome-Manager-Clean).
//
// Core idea used by that tool's sync assistant:
//  1. Map pointer with OUTER window rect proportions (GetWindowRect), not only client area.
//  2. Match extension/wallet popups by size + title Jaccard + relative offset to parent.
//  3. Throttle mouse moves by time AND pixel distance so multi-window stays smooth.

// chromeManagerMapPoint maps a screen point from a master surface outer rect
// into client coordinates of a follower surface of size (fW,fH), using the same
// proportional calibration as Chrome-Manager's on_mouse_event.
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
	// CM: size_diff * (2.0 - title_sim). Keep integer micro-score for ordering.
	titlePenalty := int64(math.Round((1.0 - titleSim) * 5000))
	// Prefer title+size for wallet popups (CM title_similarity > 0.5 or size±50).
	return int64(sizeDelta)*1000 + int64(positionDelta) + titlePenalty
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
