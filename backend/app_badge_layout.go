package backend

type badgeNumberLayout struct {
	scale int
	gap   int
	fontW int
	fontH int
	pillW int
	pillH int
}

// calculateBadgeNumberLayout centers a bold white number over the full blue
// icon. The digit height targets roughly 60% of the native taskbar icon while
// retaining a readable two-pixel stroke for 100+ at common Windows sizes.
func calculateBadgeNumberLayout(size, digits int) badgeNumberLayout {
	if digits < 1 {
		digits = 1
	}
	scale := max(1, (size*3/5)/5)
	var layout badgeNumberLayout
	for {
		gap := max(1, scale/3)
		fontW := digits*3*scale + (digits-1)*gap
		fontH := 5 * scale
		layout = badgeNumberLayout{
			scale: scale,
			gap:   gap,
			fontW: fontW,
			fontH: fontH,
			pillW: size,
			pillH: size,
		}
		if fontW <= size-2 || scale == 1 {
			return layout
		}
		scale--
	}
}
