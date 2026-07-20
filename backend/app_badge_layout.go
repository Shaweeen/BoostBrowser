package backend

import "math"

type badgeNumberLayout struct {
	scale int
	gap   int
	fontW int
	fontH int
	pillW int
	pillH int
}

// calculateBadgeNumberLayout keeps the red marker at least about 40% of the
// native icon height. Three-digit labels such as 100 must remain at the same
// readable stroke scale as one- and two-digit labels whenever they fit.
func calculateBadgeNumberLayout(size, digits int) badgeNumberLayout {
	if digits < 1 {
		digits = 1
	}
	scale := max(1, (size+8)/16)
	var layout badgeNumberLayout
	for {
		gap := max(1, scale/2)
		fontW := digits*3*scale + (digits-1)*gap
		fontH := 5 * scale
		padX := max(2, scale)
		padY := max(2, scale/2)
		pillW := fontW + 2*padX
		pillH := fontH + 2*padY
		targetPillH := max(7, int(math.Ceil(float64(size)*0.44)))
		pillH = min(size, max(pillH, targetPillH))
		if digits == 1 && pillW < pillH {
			pillW = pillH
		}
		layout = badgeNumberLayout{
			scale: scale,
			gap:   gap,
			fontW: fontW,
			fontH: fontH,
			pillW: min(size, pillW),
			pillH: pillH,
		}
		if pillW <= size || scale == 1 {
			return layout
		}
		scale--
	}
}
