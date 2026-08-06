package backend

import "testing"

func TestNaturalProfileNameLessNumericOrder(t *testing.T) {
	// "10" must not sort before "2" (string order would).
	if !naturalProfileNameLess("环境2", "b", "环境10", "a") {
		t.Fatal("2 should sort before 10")
	}
	if naturalProfileNameLess("环境10", "a", "环境2", "b") {
		t.Fatal("10 should not sort before 2")
	}
	if !naturalProfileNameLess("1", "z", "2", "a") {
		t.Fatal("1 before 2")
	}
	// Equal names fall back to profileId.
	if !naturalProfileNameLess("same", "a", "same", "b") {
		t.Fatal("id tie-break a < b")
	}
}

func TestTileGridDimensionsDenseMultiOpen(t *testing.T) {
	cases := []struct {
		n, cols, rows int
	}{
		{1, 1, 1},
		{2, 2, 1},
		{4, 2, 2},
		{6, 3, 2},
		{7, 3, 3},
		{9, 3, 3},
		{10, 4, 3},
		{12, 4, 3},
		{13, 4, 4},
	}
	for _, tc := range cases {
		cols, rows := tileGridDimensions(tc.n)
		if cols != tc.cols || rows != tc.rows {
			t.Fatalf("n=%d want %dx%d got %dx%d", tc.n, tc.cols, tc.rows, cols, rows)
		}
	}
}

func TestComputeUniformTileRectsFlushGapAndLastSameSize(t *testing.T) {
	// 10 windows, 4×3, gap=0: every cell identical; last incomplete-row cell
	// must NOT stretch to remaining width.
	cols, rows := 4, 3
	rects := computeUniformTileRects(10, cols, rows, 0, 0, 1920, 1040, defaultTileGapPx)
	if len(rects) != 10 {
		t.Fatalf("want 10, got %d", len(rects))
	}
	w0, h0 := rects[0].W, rects[0].H
	// gap=0: 1920/4=480, 1040/3=346
	if w0 != 480 || h0 != 346 {
		t.Fatalf("uniform cell want 480x346 got %dx%d", w0, h0)
	}
	for i, r := range rects {
		if r.W != w0 || r.H != h0 {
			t.Fatalf("window %d must match first %dx%d, got %dx%d", i, w0, h0, r.W, r.H)
		}
	}
	// Adjacent cells abut (gap 0).
	if rects[1].X != rects[0].X+w0 {
		t.Fatalf("horizontal abut: col0.right=%d col1.x=%d", rects[0].X+w0, rects[1].X)
	}
	if rects[4].Y != rects[0].Y+h0 {
		t.Fatalf("vertical abut: row0.bottom=%d row1.y=%d", rects[0].Y+h0, rects[4].Y)
	}
	// Last window (index 9) is col1 of row2 — same size.
	if rects[9].W != w0 || rects[9].X != w0 {
		t.Fatalf("last incomplete-row window must stay uniform: %+v", rects[9])
	}
}

func TestComputeUniformTileRectsOnePixelGapOptional(t *testing.T) {
	rects := computeUniformTileRects(4, 2, 2, 0, 0, 1000, 800, 1)
	if len(rects) != 4 {
		t.Fatal(len(rects))
	}
	// (1000-1)/2=499, (800-1)/2=399
	if rects[0].W != 499 || rects[0].H != 399 {
		t.Fatalf("got %dx%d", rects[0].W, rects[0].H)
	}
	if rects[1].X != 499+1 {
		t.Fatalf("1px gap: %d", rects[1].X)
	}
}
