package backend

import (
	"math"
	"testing"
)

func TestBadgeNumberLayoutUsesAtLeastFortyPercentHeight(t *testing.T) {
	for _, size := range []int{16, 24, 32, 48, 64, 128} {
		for digits := 1; digits <= 4; digits++ {
			layout := calculateBadgeNumberLayout(size, digits)
			minimumHeight := int(math.Ceil(float64(size) * 0.40))
			if layout.pillH < minimumHeight {
				t.Fatalf("badge too small: size=%d digits=%d height=%d minimum=%d", size, digits, layout.pillH, minimumHeight)
			}
			if layout.pillW > size || layout.fontW > layout.pillW {
				t.Fatalf("badge does not fit: size=%d digits=%d layout=%+v", size, digits, layout)
			}
		}
	}
}

func TestThreeDigitBadgeKeepsReadableScaleAtTaskbarSizes(t *testing.T) {
	for _, size := range []int{24, 32, 48, 64} {
		layout := calculateBadgeNumberLayout(size, 3)
		if layout.scale < 2 {
			t.Fatalf("100+ badge scale too small: size=%d layout=%+v", size, layout)
		}
	}
}
