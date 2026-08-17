package backend

import "testing"

func TestChromeBrandVersionsFromUA(t *testing.T) {
	cases := []struct {
		name     string
		ua       string
		wantMaj  string
		wantFull string
	}{
		{
			name:     "google-148 CfT UA",
			ua:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.7778.167 Safari/537.36",
			wantMaj:  "148",
			wantFull: "148.0.7778.167",
		},
		{
			name:     "overridden UA from launch args keeps real version",
			ua:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.7778.167 Safari/537.36",
			wantMaj:  "148",
			wantFull: "148.0.7778.167",
		},
		{
			name:     "older kernel fallback",
			ua:       "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.7680.177 Safari/537.36",
			wantMaj:  "146",
			wantFull: "146.0.7680.177",
		},
		{
			name:     "unparseable UA falls back to current built-in kernel",
			ua:       "Mozilla/5.0 (compatible; SomeBot)",
			wantMaj:  "148",
			wantFull: "148.0.7778.167",
		},
		{
			name:     "empty UA falls back to current built-in kernel",
			ua:       "",
			wantMaj:  "148",
			wantFull: "148.0.7778.167",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			maj, full := chromeBrandVersionsFromUA(tc.ua)
			if maj != tc.wantMaj {
				t.Errorf("major = %q, want %q", maj, tc.wantMaj)
			}
			if full != tc.wantFull {
				t.Errorf("full = %q, want %q", full, tc.wantFull)
			}
		})
	}
}
