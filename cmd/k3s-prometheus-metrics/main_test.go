package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wantNodeSelectorDefault is k3s's node-role.kubernetes.io/control-plane
// label value; kubeadm uses an empty value instead. Hardcoded so it can't
// silently drift with config.ControlPlaneNodeSelector.
const wantNodeSelectorDefault = `node-role.kubernetes.io/control-plane=true`

// TestNodeSelectorFlagDefault_MatchesK3sConvention builds the actual binary
// and checks its own -h output, so it tests what ships, not a re-declared
// copy of the default.
func TestNodeSelectorFlagDefault_MatchesK3sConvention(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "k3s-prometheus-metrics")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building binary: %v\n%s", err, out)
	}

	// -h makes flag.Parse print usage, including each flag's default.
	out, _ := exec.Command(bin, "-h").CombinedOutput()

	wantDefault := `(default "` + wantNodeSelectorDefault + `")`
	if !strings.Contains(string(out), wantDefault) {
		t.Fatalf("expected --node-selector default %s in -h output, got:\n%s", wantDefault, out)
	}
}
