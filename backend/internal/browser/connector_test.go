package browser

import (
	"reflect"
	"testing"
)

func TestBuildLaunchArgsKeepsStartupBlankByDefault(t *testing.T) {
	t.Parallel()

	baseArgs := []string{"--disable-sync"}
	got := BuildLaunchArgs(append([]string{}, baseArgs...), &Profile{})
	want := []string{"--disable-sync"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildLaunchArgs result mismatch:\n got=%v\nwant=%v", got, want)
	}
}
