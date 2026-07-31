package backend

import "testing"

func TestChromeTileFrameOverlapPxClamps(t *testing.T) {
	if got := chromeTileFrameOverlapPx(0, 0); got != 8 {
		t.Fatalf("min overlap want 8 got %d", got)
	}
	if got := chromeTileFrameOverlapPx(4, 4); got != 8 {
		t.Fatalf("4+4 should floor to 8, got %d", got)
	}
	if got := chromeTileFrameOverlapPx(8, 4); got != 12 {
		t.Fatalf("8+4 want 12 got %d", got)
	}
	if got := chromeTileFrameOverlapPx(20, 10); got != 14 {
		t.Fatalf("must cap at 14, got %d", got)
	}
}

func TestComputeGaplessTileRectsUniformSize(t *testing.T) {
	// 3 columns: every window must share the same W×H (no remainder stretch).
	rects := computeGaplessTileRects(3, 3, 1, 0, 0, 1000, 800, 0, 0)
	if len(rects) != 3 {
		t.Fatalf("want 3 rects, got %d", len(rects))
	}
	w0, h0 := rects[0].W, rects[0].H
	for i, r := range rects {
		if r.W != w0 || r.H != h0 {
			t.Fatalf("window %d size %dx%d != first %dx%d", i, r.W, r.H, w0, h0)
		}
	}
	// Floor division: 1000/3 = 333 each; leftover 1px strip is OK.
	if w0 != 333 || h0 != 800 {
		t.Fatalf("uniform cell want 333x800 got %dx%d", w0, h0)
	}
	// Adjacent cells abut without stretch.
	if rects[1].X != rects[0].X+w0 {
		t.Fatalf("col1 should start at %d, got %d", rects[0].X+w0, rects[1].X)
	}
}

func TestComputeGaplessTileRectsInternalOverlapKeepsEqualSize(t *testing.T) {
	overlap := 8
	rects := computeGaplessTileRects(2, 2, 1, 0, 0, 1000, 800, overlap, 0)
	if len(rects) != 2 {
		t.Fatal(len(rects))
	}
	if rects[0].W != rects[1].W || rects[0].H != rects[1].H {
		t.Fatalf("sizes must match: %+v %+v", rects[0], rects[1])
	}
	// Each is cellW(500)+2*overlap wide.
	if rects[0].W != 500+2*overlap {
		t.Fatalf("width want %d got %d", 500+2*overlap, rects[0].W)
	}
	// Overlap band between them.
	span := (rects[0].X + rects[0].W) - rects[1].X
	if span != 2*overlap {
		t.Fatalf("overlap span want %d got %d", 2*overlap, span)
	}
}

func TestComputeGaplessTileRectsOuterBleedUniform(t *testing.T) {
	bleed := 8
	rects := computeGaplessTileRects(1, 1, 1, 10, 20, 1000, 800, 0, bleed)
	if len(rects) != 1 {
		t.Fatal(len(rects))
	}
	// Single cell: origin - margin, size cell+2*margin.
	if rects[0].X != 10-bleed || rects[0].Y != 20-bleed {
		t.Fatalf("outer origin: %+v", rects[0])
	}
	if rects[0].W != 1000+2*bleed || rects[0].H != 800+2*bleed {
		t.Fatalf("outer size: %+v", rects[0])
	}
}

func TestComputeGaplessTileRectsIncompleteLastRowKeepsUniformCell(t *testing.T) {
	// 3 windows in 2×2: last row has one window — must stay half-width cell,
	// NOT stretch to full work-area width (that broke sync coordinate ratios).
	rects := computeGaplessTileRects(3, 2, 2, 0, 0, 1000, 800, 0, 0)
	if len(rects) != 3 {
		t.Fatalf("want 3, got %d", len(rects))
	}
	w0, h0 := rects[0].W, rects[0].H
	for i, r := range rects {
		if r.W != w0 || r.H != h0 {
			t.Fatalf("window %d must match first size %dx%d, got %dx%d", i, w0, h0, r.W, r.H)
		}
	}
	// cell 500×400; last window only occupies col0 of row1.
	if rects[2].X != 0 || rects[2].W != 500 {
		t.Fatalf("last row must keep uniform half-width cell: %+v", rects[2])
	}
	if rects[2].W == 1000 {
		t.Fatal("must not stretch last window to full width")
	}
}

func TestComputeGaplessTileRectsVerticalStackEqual(t *testing.T) {
	rects := computeGaplessTileRects(3, 1, 3, 0, 0, 800, 900, 8, 0)
	if len(rects) != 3 {
		t.Fatal(len(rects))
	}
	for i := 1; i < 3; i++ {
		if rects[i].W != rects[0].W || rects[i].H != rects[0].H {
			t.Fatalf("stack sizes must match: %+v vs %+v", rects[0], rects[i])
		}
	}
	// Vertical neighbors overlap.
	if rects[1].Y >= rects[0].Y+rects[0].H {
		t.Fatalf("expected vertical overlap: %+v %+v", rects[0], rects[1])
	}
}
