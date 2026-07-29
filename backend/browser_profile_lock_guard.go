package backend

import (
	"os"
	"path/filepath"
)

var browserSingletonArtifactNames = []string{
	"SingletonLock",
	"SingletonCookie",
	"SingletonSocket",
}

func browserSingletonArtifactsPresent(userDataDir string) bool {
	for _, name := range browserSingletonArtifactNames {
		if _, err := os.Lstat(filepath.Join(userDataDir, name)); err == nil {
			return true
		} else if !os.IsNotExist(err) {
			// An unreadable artifact must take the guarded path so launch does not
			// bypass the ownership check and risk opening one profile twice.
			return true
		}
	}
	return false
}
