package backend

import "testing"

func TestChromeManagerMapPointProportional(t *testing.T) {
	// Master 0,0-1000x500; point at center → follower 200x100 center.
	cx, cy, ok := chromeManagerMapPoint(500, 250, 0, 0, 1000, 500, 200, 100)
	if !ok || cx != 100 || cy != 50 {
		t.Fatalf("center map: ok=%v cx=%d cy=%d", ok, cx, cy)
	}
	// Top-left corner.
	cx, cy, ok = chromeManagerMapPoint(0, 0, 0, 0, 1000, 500, 200, 100)
	if !ok || cx != 0 || cy != 0 {
		t.Fatalf("origin map: ok=%v cx=%d cy=%d", ok, cx, cy)
	}
	// Outside should fail (beyond slack).
	if _, _, ok = chromeManagerMapPoint(-50, 0, 0, 0, 1000, 500, 200, 100); ok {
		t.Fatal("far outside must fail")
	}
}

func TestTitleSimilarityJaccard(t *testing.T) {
	if titleSimilarityJaccard("MetaMask", "MetaMask") != 1 {
		t.Fatal("exact match")
	}
	if titleSimilarityJaccard("", "") != 1 {
		t.Fatal("empty/empty")
	}
	if titleSimilarityJaccard("MetaMask Notification", "") != 0 {
		t.Fatal("empty other")
	}
	sim := titleSimilarityJaccard("MetaMask", "MetaMask Notification")
	if sim <= 0.3 {
		t.Fatalf("related titles should score higher, got %v", sim)
	}
	if titleSimilarityJaccard("MetaMask", "Example - Google Chrome") >= sim {
		t.Fatal("unrelated title must score worse than related")
	}
}

func TestChromeManagerPopupMatchScorePrefersTitleAndSize(t *testing.T) {
	// Same size+title near expected position beats wrong size.
	good := chromeManagerPopupMatchScore(320, 680, 320, 680, 100, 80, 100, 80, "MetaMask", "MetaMask")
	bad := chromeManagerPopupMatchScore(320, 680, 180, 60, 100, 80, 100, 80, "MetaMask", "Chrome")
	if good >= bad {
		t.Fatalf("good=%d should beat bad=%d", good, bad)
	}
}

func TestShouldThrottleMouseMoveByDistance(t *testing.T) {
	if !shouldThrottleMouseMoveByDistance(10, 10, 11, 10, 2) {
		t.Fatal("1px move must throttle at threshold 2")
	}
	if shouldThrottleMouseMoveByDistance(10, 10, 20, 10, 2) {
		t.Fatal("10px move must pass")
	}
}

func TestExpectedPopupOffset(t *testing.T) {
	// Master at 0, popup at 100 → follower at 1000 should place popup at 1100.
	if got := expectedPopupOffsetLeft(100, 0, 1000); got != 1100 {
		t.Fatalf("left offset: %d", got)
	}
	if got := expectedPopupOffsetTop(50, 0, 200); got != 250 {
		t.Fatalf("top offset: %d", got)
	}
}
