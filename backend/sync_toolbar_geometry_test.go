package backend

import "testing"

func TestNativeClickReleaseStaysOnToolbarWhenPopupAppears(t *testing.T) {
	s := make(nativeMouseSequence)
	toolbar := nativeMouseTarget{owner: 1, window: 1, point: 65, pid: 100}
	pressed := s.resolve(1, true, 0, func() []nativeMouseTarget { return []nativeMouseTarget{toolbar} })
	released := s.resolve(1, false, 0, func() []nativeMouseTarget {
		t.Fatal("release must not rediscover the newly opened wallet popup")
		return nil
	})
	if len(pressed) != 1 || len(released) != 1 || released[0] != toolbar {
		t.Fatalf("press/release changed surfaces: %v / %v", pressed, released)
	}
	if again := s.resolve(1, false, 0, nil); len(again) != 0 {
		t.Fatal("duplicate release must not dispatch")
	}
}

func TestNativeClickDoesNotCrossPauseOrLayoutGeneration(t *testing.T) {
	s := make(nativeMouseSequence)
	s.resolve(1, true, 0, func() []nativeMouseTarget { return []nativeMouseTarget{{window: 1}} })
	if got := s.resolve(1, false, 1, nil); len(got) != 0 {
		t.Fatal("old click must not execute against a new layout/session generation")
	}
}

func TestToolbarExtensionClickDoesNotScaleIntoCaption(t *testing.T) {
	// The retired whole-window mapping moves a toolbar click at y=65 to y=20
	// when a 1400x900 master drives a 460x280 tile (the caption-button row).
	_, oldY, ok := chromeManagerMapPoint(1320, 65, 0, 0, 1400, 900, 460, 280)
	if !ok || oldY != 20 {
		t.Fatalf("regression fixture changed: y=%d ok=%v", oldY, ok)
	}
	x, y, ok := mapChromeToolbarPoint(1320, 65, 1400, 460, 100, 100, 96, 96)
	if !ok || x != 380 || y != 65 {
		t.Fatalf("extension must retain its row and distance from right edge: %d,%d %v", x, y, ok)
	}
}

func TestToolbarMappingDPIAndBounds(t *testing.T) {
	tests := []struct {
		name                         string
		x, y, mw, fw, mt, ft, md, fd int
		wantX, wantY                 int
		ok                           bool
	}{
		{"same size", 1320, 65, 1400, 1400, 100, 100, 96, 96, 1320, 65, true},
		{"150 percent follower", 1320, 64, 1400, 690, 100, 150, 96, 144, 570, 96, true},
		{"left navigation", 40, 64, 1400, 460, 100, 100, 96, 96, 40, 64, true},
		{"content excluded", 100, 100, 1400, 460, 100, 100, 96, 96, 0, 0, false},
		{"nonclient excluded", 100, -1, 1400, 460, 100, 100, 96, 96, 0, 0, false},
		{"cannot fit", 710, 65, 1400, 460, 100, 100, 96, 96, 0, 0, false},
		{"missing render", 1320, 65, 1400, 460, 100, 0, 96, 96, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, y, ok := mapChromeToolbarPoint(tt.x, tt.y, tt.mw, tt.fw, tt.mt, tt.ft, tt.md, tt.fd)
			if x != tt.wantX || y != tt.wantY || ok != tt.ok {
				t.Fatalf("got (%d,%d,%v), want (%d,%d,%v)", x, y, ok, tt.wantX, tt.wantY, tt.ok)
			}
		})
	}
}
