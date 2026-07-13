package activation

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Provider deliberately separates the current offline installer bootstrap from
// future signed-license or server-backed activation. Callers depend only on
// Verify, so a later provider can be introduced without changing app startup.
type Provider interface {
	Verify(appRoot string) Status
}

type Status struct {
	Active   bool   `json:"active"`
	Provider string `json:"provider"`
	Reason   string `json:"reason,omitempty"`
}

type installerMarker struct {
	Scheme string `json:"scheme"`
	Proof  string `json:"proof"`
}

type OfflineInstallerProvider struct{}

const (
	markerName = ".browserstudio-activation.json"
	// Only a one-way verifier is shipped. The installer build derives this value
	// from its private input; the original installation variable is not stored in
	// application configuration or emitted to logs.
	expectedProofHex = "a2791bcfba6d3b01cf232386a276951c921ce6c743030a3298e646c8dceabb65"
)

func (OfflineInstallerProvider) Verify(appRoot string) Status {
	data, err := os.ReadFile(filepath.Join(appRoot, markerName))
	if err != nil {
		return Status{Provider: "offline-installer-v1", Reason: "marker_missing"}
	}
	var marker installerMarker
	if json.Unmarshal(data, &marker) != nil || marker.Scheme != "offline-installer-v1" {
		return Status{Provider: "offline-installer-v1", Reason: "marker_invalid"}
	}
	want, _ := hex.DecodeString(expectedProofHex)
	got, err := hex.DecodeString(strings.TrimSpace(marker.Proof))
	if err != nil || len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		return Status{Provider: "offline-installer-v1", Reason: "proof_invalid"}
	}
	return Status{Active: true, Provider: "offline-installer-v1"}
}

func DeriveInstallerProof(seed string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("browserstudio-install:%s:v1", strings.TrimSpace(seed))))
	return hex.EncodeToString(sum[:])
}

func ValidateInstallerSeed(seed string) bool {
	want, _ := hex.DecodeString(expectedProofHex)
	got, err := hex.DecodeString(DeriveInstallerProof(seed))
	return err == nil && len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1
}

func WriteInstallerMarker(path string) error {
	marker, err := json.Marshal(installerMarker{Scheme: "offline-installer-v1", Proof: expectedProofHex})
	if err != nil {
		return err
	}
	return os.WriteFile(path, marker, 0o600)
}
