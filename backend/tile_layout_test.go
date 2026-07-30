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

func TestComputeGaplessTileRectsFillsWorkAreaWithoutRemainderStrip(t *testing.T) {
	// 3 columns into 1000px: 334+333+333 = 1000; no leftover strip.
	rects := computeGaplessTileRects(3, 3, 1, 0, 0, 1000, 800, 0, 0)
	if len(rects) != 3 {
		t.Fatalf("want 3 rects, got %d", len(rects))
	}
	right := rects[2].X + rects[2].W
	if right != 1000 {
		t.Fatalf("last right edge want 1000 got %d (rects=%+v)", right, rects)
	}
	// Adjacent cells must touch when overlap=0.
	if rects[0].X+rects[0].W != rects[1].X {
		t.Fatalf("gap between col0 and col1: %+v %+v", rects[0], rects[1])
	}
}

func TestComputeGaplessTileRectsInternalOverlap(t *testing.T) {
	overlap := 8
	rects := computeGaplessTileRects(2, 2, 1, 0, 0, 1000, 800, overlap, 0)
	if len(rects) != 2 {
		t.Fatal(len(rects))
	}
	// Right edge of first extends into second by overlap.
	if rects[0].X+rects[0].W != 500+overlap {
		t.Fatalf("left window right edge: got %d want %d", rects[0].X+rects[0].W, 500+overlap)
	}
	if rects[1].X != 500-overlap {
		t.Fatalf("right window left edge: got %d want %d", rects[1].X, 500-overlap)
	}
	// Overlap region width = 2*overlap (each side grows by overlap).
	// Wait: only the right window shifts left, left window extends right...
	// left: left=0, right=500+overlap → width 500+overlap
	// right: left=500-overlap, right=1000 → width 500+overlap
	// overlap span = (500+overlap) - (500-overlap) = 2*overlap
	// Actually looking at code: if col>0 left-=overlap; if col+1<cols right+=overlap
	// left window col=0: left=0, right=500+overlap
	// right window col=1: left=500-overlap, right=1000
	// overlap = 2*overlap pixels. That's intentional for thick dual borders.
	span := (rects[0].X + rects[0].W) - rects[1].X
	if span != 2*overlap {
		t.Fatalf("overlap span want %d got %d", 2*overlap, span)
	}
}

func TestComputeGaplessTileRectsOuterBleed(t *testing.T) {
	bleed := 8
	rects := computeGaplessTileRects(1, 1, 1, 10, 20, 1000, 800, 0, bleed)
	if len(rects) != 1 {
		t.Fatal(len(rects))
	}
	if rects[0].X != 10-bleed || rects[0].Y != 20-bleed {
		t.Fatalf("outer origin bleed: %+v", rects[0])
	}
	if rects[0].W != 1000+2*bleed || rects[0].H != 800+2*bleed {
		t.Fatalf("outer size bleed: %+v", rects[0])
	}
}

func TestComputeGaplessTileRectsStretchesIncompleteLastRow(t *testing.T) {
	// 3 windows in 2×2: last row has 1 window and must span full width.
	rects := computeGaplessTileRects(3, 2, 2, 0, 0, 1000, 800, 0, 0)
	if len(rects) != 3 {
		t.Fatalf("want 3, got %d", len(rects))
	}
	last := rects[2]
	if last.X != 0 || last.W != 1000 {
		t.Fatalf("last row sole window must span full width: %+v", last)
	}
	// First row still half-and-half.
	if rects[0].W != 500 || rects[1].X != 500 {
		t.Fatalf("first row split: %+v %+v", rects[0], rects[1])
	}
}

func TestComputeGaplessTileRectsVerticalStack(t *testing.T) {
	rects := computeGaplessTileRects(3, 1, 3, 0, 0, 800, 900, 8, 0)
	if len(rects) != 3 {
		t.Fatal(len(rects))
	}
	// Vertical neighbors overlap.
	if rects[1].Y >= rects[0].Y+rects[0].H {
		t.Fatalf("expected vertical overlap: %+v %+v", rects[0], rects[1])
	}
}
