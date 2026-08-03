package backend

import (
	"strings"
	"unicode"
)

// tileLayoutRect is one environment window outer rect in screen pixels.
type tileLayoutRect struct {
	X, Y, W, H int
}

// naturalProfileNameLess orders environments by display name with numeric
// awareness (1,2,10…) then profileId — stable grid slots under multi-open.
func naturalProfileNameLess(nameA, idA, nameB, idB string) bool {
	a := strings.TrimSpace(nameA)
	if a == "" {
		a = idA
	}
	b := strings.TrimSpace(nameB)
	if b == "" {
		b = idB
	}
	ia, ja := 0, 0
	ra, rb := []rune(a), []rune(b)
	for ia < len(ra) && ja < len(rb) {
		da := unicode.IsDigit(ra[ia])
		db := unicode.IsDigit(rb[ja])
		if da && db {
			// Skip leading zeros then compare numeric values by digit length first.
			for ia < len(ra) && ra[ia] == '0' {
				ia++
			}
			for ja < len(rb) && rb[ja] == '0' {
				ja++
			}
			sa, sb := ia, ja
			for ia < len(ra) && unicode.IsDigit(ra[ia]) {
				ia++
			}
			for ja < len(rb) && unicode.IsDigit(rb[ja]) {
				ja++
			}
			if (ia - sa) != (ja - sb) {
				return (ia - sa) < (ja - sb)
			}
			for k := 0; k < ia-sa; k++ {
				if ra[sa+k] != rb[sb+k] {
					return ra[sa+k] < rb[sb+k]
				}
			}
			continue
		}
		if da != db {
			// Digits sort before non-digits at the same position.
			return da
		}
		ca, cb := unicode.ToLower(ra[ia]), unicode.ToLower(rb[ja])
		if ca != cb {
			return ca < cb
		}
		ia++
		ja++
	}
	if len(ra)-ia != len(rb)-ja {
		return len(ra)-ia < len(rb)-ja
	}
	return idA < idB
}

// tileGridDimensions returns cols/rows for the standard multi-open grid.
// 7–9 use 3×3 (larger cells on 1080p); 10+ use 4 columns.
func tileGridDimensions(n int) (cols, rows int) {
	if n <= 0 {
		return 0, 0
	}
	if n <= 2 {
		return n, 1
	}
	if n <= 4 {
		return 2, (n + 1) / 2
	}
	if n <= 6 {
		return 3, 2
	}
	if n <= 9 {
		return 3, 3
	}
	return 4, (n + 3) / 4
}

// defaultTileGapPx is the fixed pixel gap between adjacent tiled environments.
// User-facing multi-open layout uses a 1px seam (not large DWM overlap).
const defaultTileGapPx = 1

// computeUniformTileRects places n windows into a cols×rows grid with **identical
// outer W×H for every window** (including incomplete last-row cells) and a fixed
// gap between neighbors. Leftover work-area strip at right/bottom is unused —
// never stretch the last window (that broke sync click/scroll proportions).
func computeUniformTileRects(n, cols, rows, originX, originY, screenW, screenH, gapPx int) []tileLayoutRect {
	if n <= 0 || cols <= 0 || rows <= 0 || screenW <= 0 || screenH <= 0 {
		return nil
	}
	if gapPx < 0 {
		gapPx = 0
	}
	// Usable area after inter-cell gaps: (cols-1) horizontal + (rows-1) vertical.
	gapTotalW := gapPx * (cols - 1)
	gapTotalH := gapPx * (rows - 1)
	if gapTotalW >= screenW || gapTotalH >= screenH {
		gapPx = 0
		gapTotalW = 0
		gapTotalH = 0
	}
	cellW := (screenW - gapTotalW) / cols
	cellH := (screenH - gapTotalH) / rows
	if cellW < 1 {
		cellW = 1
	}
	if cellH < 1 {
		cellH = 1
	}

	out := make([]tileLayoutRect, 0, n)
	for i := 0; i < n; i++ {
		col := i % cols
		row := i / cols
		if row >= rows {
			break
		}
		x := originX + col*(cellW+gapPx)
		y := originY + row*(cellH+gapPx)
		out = append(out, tileLayoutRect{X: x, Y: y, W: cellW, H: cellH})
	}
	return out
}

// computeGaplessTileRects is the historical overlap/bleed tile helper kept for
// tests and call-site compatibility. Production multi-open uses
// computeUniformTileRects with defaultTileGapPx=1.
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
	// When both overlap args are zero, behave like a 0-gap uniform grid.
	if frameOverlapPx == 0 && outerBleedPx == 0 {
		return computeUniformTileRects(n, cols, rows, originX, originY, screenW, screenH, 0)
	}
	margin := frameOverlapPx
	if outerBleedPx > margin {
		margin = outerBleedPx
	}
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

	cellW := screenW / cols
	cellH := screenH / rows
	if cellW < 1 {
		cellW = 1
	}
	if cellH < 1 {
		cellH = 1
	}

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
		x := originX + col*cellW - marginX
		y := originY + row*cellH - marginY
		out = append(out, tileLayoutRect{X: x, Y: y, W: winW, H: winH})
	}
	return out
}

// chromeTileFrameOverlapPx is retained for diagnostics; production tile uses a
// fixed 1px gap instead of large DWM frame overlap.
func chromeTileFrameOverlapPx(smCXFrame, smCXPAddedBorder int) int {
	overlap := smCXFrame + smCXPAddedBorder
	if overlap < 8 {
		overlap = 8
	}
	if overlap > 14 {
		overlap = 14
	}
	return overlap
}
