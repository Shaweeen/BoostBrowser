package backend

// tileLayoutRect is one environment window outer rect in screen pixels.
type tileLayoutRect struct {
	X, Y, W, H int
}

// computeGaplessTileRects places n windows into a cols×rows grid covering the
// work area with no leftover strip. Adjacent cells share an internal overlap so
// Chrome/DWM non-client borders do not leave a visible seam. Outer edges bleed
// past the work area to cover edge shadows. Incomplete last rows stretch so the
// remaining windows still fill full work-area width.
//
// frameOverlapPx: how far adjacent windows overlap each other (typically 7–12).
// outerBleedPx: how far outer edges extend past the work area (typically 8).
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
	// Cap so tiny cells (many environments) still keep usable content area.
	maxOverlap := screenW / (cols * 3)
	if maxOverlap < 1 {
		maxOverlap = 1
	}
	if frameOverlapPx > maxOverlap {
		frameOverlapPx = maxOverlap
	}
	maxOverlapH := screenH / (rows * 3)
	if maxOverlapH < 1 {
		maxOverlapH = 1
	}
	overlapY := frameOverlapPx
	if overlapY > maxOverlapH {
		overlapY = maxOverlapH
	}
	overlapX := frameOverlapPx

	colWidths := make([]int, cols)
	rowHeights := make([]int, rows)
	baseW, remW := screenW/cols, screenW%cols
	baseH, remH := screenH/rows, screenH%rows
	for c := 0; c < cols; c++ {
		colWidths[c] = baseW
		if c < remW {
			colWidths[c]++
		}
	}
	for r := 0; r < rows; r++ {
		rowHeights[r] = baseH
		if r < remH {
			rowHeights[r]++
		}
	}
	colX := make([]int, cols)
	rowY := make([]int, rows)
	colX[0] = originX
	for c := 1; c < cols; c++ {
		colX[c] = colX[c-1] + colWidths[c-1]
	}
	rowY[0] = originY
	for r := 1; r < rows; r++ {
		rowY[r] = rowY[r-1] + rowHeights[r-1]
	}

	out := make([]tileLayoutRect, 0, n)
	for i := 0; i < n; i++ {
		col := i % cols
		row := i / cols
		if row >= rows {
			break
		}

		// Last incomplete row: stretch remaining windows across full width so
		// the empty trailing cell does not look like a large gap.
		rowStart := row * cols
		rowCount := cols
		if rowStart+rowCount > n {
			rowCount = n - rowStart
		}
		useStretchedRow := rowCount > 0 && rowCount < cols
		var left, top, right, bottom int
		if useStretchedRow {
			// Redistribute this row's width among the windows that exist.
			stretchW := make([]int, rowCount)
			baseSW, remSW := screenW/rowCount, screenW%rowCount
			for c := 0; c < rowCount; c++ {
				stretchW[c] = baseSW
				if c < remSW {
					stretchW[c]++
				}
			}
			stretchX := originX
			for c := 0; c < col; c++ {
				stretchX += stretchW[c]
			}
			left = stretchX
			right = left + stretchW[col]
			// Horizontal neighbors within the stretched row.
			if col > 0 {
				left -= overlapX
			} else {
				left -= outerBleedPx
			}
			if col+1 < rowCount {
				right += overlapX
			} else {
				right += outerBleedPx
			}
		} else {
			left = colX[col]
			right = left + colWidths[col]
			if col > 0 {
				left -= overlapX
			} else {
				left -= outerBleedPx
			}
			if col+1 < cols {
				right += overlapX
			} else {
				right += outerBleedPx
			}
		}

		top = rowY[row]
		bottom = top + rowHeights[row]
		if row > 0 {
			top -= overlapY
		} else {
			top -= outerBleedPx
		}
		if row+1 < rows {
			bottom += overlapY
		} else {
			bottom += outerBleedPx
		}

		w := right - left
		h := bottom - top
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
		out = append(out, tileLayoutRect{X: left, Y: top, W: w, H: h})
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
