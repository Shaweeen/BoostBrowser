package backend

// tileLayoutRect is one environment window outer rect in screen pixels.
type tileLayoutRect struct {
	X, Y, W, H int
}

// computeGaplessTileRects places n windows into a cols×rows grid with **uniform
// cell sizes**. Every environment gets the same outer width/height so input-sync
// proportional calibration stays accurate.
//
// Incomplete last rows intentionally leave empty cells empty — they must NOT be
// stretched to fill the work area (that made the last window wider and broke
// multi-env coordinate alignment).
//
// frameOverlapPx: equal inset/outset applied to every cell edge so adjacent
// frames overlap without changing relative size equality.
// outerBleedPx: unused for size differentiation; kept for API compatibility and
// applied uniformly as additional equal margin (max with frameOverlap).
func computeGaplessTileRects(n, cols, rows, originX, originY, screenW, screenH, frameOverlapPx, outerBleedPx int) []tileLayoutRect {
	if n <= 0 || cols <= 0 || rows <= 0 || screenW <= 0 || screenH <= 0 {
		return nil
	}
	if frameOverlapPx < 0 {
		frameOverlapPx = 0
	}
	if outerBleedPx < 0 {
		outerBleedPx = 0
	}
	// Uniform edge expand: same for every window so W/H stay identical.
	// Use the larger of frame overlap and outer bleed as a single equal margin.
	margin := frameOverlapPx
	if outerBleedPx > margin {
		margin = outerBleedPx
	}
	// Cap so tiny cells still keep usable content.
	maxMargin := screenW / (cols * 3)
	if maxMargin < 1 {
		maxMargin = 1
	}
	if margin > maxMargin {
		margin = maxMargin
	}
	maxMarginH := screenH / (rows * 3)
	if maxMarginH < 1 {
		maxMarginH = 1
	}
	marginY := margin
	if marginY > maxMarginH {
		marginY = maxMarginH
	}
	marginX := margin

	// Integer floor: leftover strip at right/bottom is fine. Do not give remainder
	// pixels to early columns — that made col0 one pixel wider than colN-1.
	cellW := screenW / cols
	cellH := screenH / rows
	if cellW < 1 {
		cellW = 1
	}
	if cellH < 1 {
		cellH = 1
	}

	// Every window shares the same outer size (cell + equal margin on all sides).
	winW := cellW + 2*marginX
	winH := cellH + 2*marginY
	if winW < 1 {
		winW = 1
	}
	if winH < 1 {
		winH = 1
	}

	out := make([]tileLayoutRect, 0, n)
	for i := 0; i < n; i++ {
		col := i % cols
		row := i / cols
		if row >= rows {
			break
		}
		// Cell origin; margin expands equally so neighbors share an overlap band.
		x := originX + col*cellW - marginX
		y := originY + row*cellH - marginY
		out = append(out, tileLayoutRect{X: x, Y: y, W: winW, H: winH})
	}
	return out
}

// chromeTileFrameOverlapPx estimates how many pixels of outer-frame overlap hide
// Chrome/DWM resize borders between adjacent tiled environments.
func chromeTileFrameOverlapPx(smCXFrame, smCXPAddedBorder int) int {
	// SM_CXFRAME + SM_CXPADDEDBORDER is the typical Win32 non-client border.
	// Chrome often paints a similar dark edge; 8px is the historical bleed that
	// looked flush on 100–150% DPI. Clamp for tiny cells / unusual metrics.
	overlap := smCXFrame + smCXPAddedBorder
	if overlap < 8 {
		overlap = 8
	}
	if overlap > 14 {
		overlap = 14
	}
	return overlap
}
