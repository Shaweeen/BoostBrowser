package activation

import "testing"

func TestInstallerProofCompatibility(t *testing.T) {
	first := DeriveInstallerProof("test-seed")
	second := DeriveInstallerProof("test-seed")
	if first == "" || first != second {
		t.Fatalf("installer proof must be stable: first=%q second=%q", first, second)
	}
}
