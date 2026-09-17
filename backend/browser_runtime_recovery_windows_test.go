//go:build windows

package backend

import "testing"

func TestParseChromeRuntimeCommandLine(t *testing.T) {
	cmd := `"Z:\Boost Browser\chrome\cloak\chrome.exe" "--user-data-dir=Z:\Boost Browser\data\profile-1" --remote-debugging-port=55089 --no-first-run`
	userDataDir, debugPort := parseChromeRuntimeCommandLine(cmd)
	if userDataDir != `Z:\Boost Browser\data\profile-1` {
		t.Fatalf("unexpected user data dir: %q", userDataDir)
	}
	if debugPort != 55089 {
		t.Fatalf("unexpected debug port: %d", debugPort)
	}
}

func TestParseChromeRuntimeCommandLineUnquotedUserDataDir(t *testing.T) {
	cmd := `C:\chrome.exe --remote-debugging-port=61234 --user-data-dir=C:\Temp\profile --window-size=1280,900`
	userDataDir, debugPort := parseChromeRuntimeCommandLine(cmd)
	if userDataDir != `C:\Temp\profile` {
		t.Fatalf("unexpected user data dir: %q", userDataDir)
	}
	if debugPort != 61234 {
		t.Fatalf("unexpected debug port: %d", debugPort)
	}
}

func TestNormalizeRuntimePathKeyIgnoresCaseAndQuotes(t *testing.T) {
	left := normalizeRuntimePathKey(`"Z:\Boost Browser\data\profile-1\"`)
	right := normalizeRuntimePathKey(`z:\boost browser\data\profile-1`)
	if left != right {
		t.Fatalf("normalized keys differ: %q vs %q", left, right)
	}
}

func TestRuntimeBrowserRootPIDPrefersDevToolsListener(t *testing.T) {
	if got := runtimeBrowserRootPID(winProcessSnapshot{ProcessId: 3111, ListeningProcessId: 2444}); got != 2444 {
		t.Fatalf("root PID=%d want listener PID 2444", got)
	}
	if got := runtimeBrowserRootPID(winProcessSnapshot{ProcessId: 3111}); got != 3111 {
		t.Fatalf("root PID=%d want command-line PID 3111 fallback", got)
	}
}
