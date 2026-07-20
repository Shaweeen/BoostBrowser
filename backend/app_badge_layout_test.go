package backend

import (
	"testing"
)

func TestBadgeNumberLayoutUsesLargeCenteredNumber(t *testing.T) {
	for _, size := range []int{24, 32, 48, 64, 128} {
		for digits := 1; digits <= 3; digits++ {
			layout := calculateBadgeNumberLayout(size, digits)
			if layout.fontH*100 < size*40 {
				t.Fatalf("number too small: size=%d digits=%d layout=%+v", size, digits, layout)
			}
			if layout.fontW > size || layout.fontH > size {
				t.Fatalf("number does not fit: size=%d digits=%d layout=%+v", size, digits, layout)
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
