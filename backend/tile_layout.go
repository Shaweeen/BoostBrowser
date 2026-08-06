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
// 0 = flush grid (sub-pixel “~0.15px” hairline is DWM only; Win32 cannot place
// fractional gaps). Vertical and horizontal use the same value.
const defaultTileGapPx = 0

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
