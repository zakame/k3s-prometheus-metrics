package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestRunManifests_InvalidNodeSelectorViaEnv_ExitsNonZeroWithError guards
// that the env-var fallback actually reaches configFlags.build, through the
// same real built binary as the CLI-flag equivalent above.
func TestRunManifests_InvalidNodeSelectorViaEnv_ExitsNonZeroWithError(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "manifests")
	cmd.Env = append(os.Environ(), "K3S_PROMETHEUS_METRICS_NODE_SELECTOR=nonsense")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if !strings.Contains(string(out), "invalid --node-selector") {
		t.Fatalf("expected output to mention --node-selector, got:\n%s", out)
	}
	if !strings.Contains(string(out), wantInvalidNodeSelectorSubstring) {
		t.Fatalf("expected output to contain %q, got:\n%s", wantInvalidNodeSelectorSubstring, out)
	}
}

// TestRunManifests_CLINodeSelectorOverridesInvalidEnv guards that an
// explicit --node-selector on the command line wins over a broken env
// value for the same flag, per applyEnvDefaults' documented precedence.
func TestRunManifests_CLINodeSelectorOverridesInvalidEnv(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "manifests", "-node-selector=a=b")
	cmd.Env = append(os.Environ(), "K3S_PROMETHEUS_METRICS_NODE_SELECTOR=nonsense")
	// Whether this succeeds depends on cluster reachability, which this test
	// doesn't control -- only assert on what it's actually checking: the
	// broken env value must not surface as an error once the CLI overrides it.
	out, _ := cmd.CombinedOutput()

	if strings.Contains(string(out), "invalid --node-selector") {
		t.Fatalf("expected the explicit CLI flag to override the broken env value, got:\n%s", out)
	}
}

// wantInvalidNodeSelectorSubstring is parseSelector's error text for an
// unparseable segment, minus its quoted segment value: runController logs
// it through zap's JSON encoder, which backslash-escapes the quotes around
// "nonsense", so a substring check can't include them. Both runController
// and runManifests reach this text through the same configFlags.build
// call, so a regression in the shared parsing would surface identically
// through either entrypoint's stderr.
const wantInvalidNodeSelectorSubstring = `expected key=value`

// TestRunController_InvalidNodeSelector_ExitsNonZeroWithError guards that an
// unparseable --node-selector reaches the user as a non-zero exit and a
// recognizable error, through the top-level entrypoint's shared
// configFlags.build call.
func TestRunController_InvalidNodeSelector_ExitsNonZeroWithError(t *testing.T) {
	bin := buildBinary(t)

	out, err := exec.Command(bin, "-node-selector=nonsense").CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if !strings.Contains(string(out), "invalid --node-selector") {
		t.Fatalf("expected output to mention --node-selector, got:\n%s", out)
	}
	if !strings.Contains(string(out), wantInvalidNodeSelectorSubstring) {
		t.Fatalf("expected output to contain %q, got:\n%s", wantInvalidNodeSelectorSubstring, out)
	}
}

// TestRunManifests_InvalidNodeSelector_ExitsNonZeroWithError is the same
// guard as TestRunController_InvalidNodeSelector_ExitsNonZeroWithError, but
// through the "manifests" subcommand's own FlagSet, since it shares the same
// configFlags.build call rather than duplicating the parsing.
func TestRunManifests_InvalidNodeSelector_ExitsNonZeroWithError(t *testing.T) {
	bin := buildBinary(t)

	out, err := exec.Command(bin, "manifests", "-node-selector=nonsense").CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if !strings.Contains(string(out), "invalid --node-selector") {
		t.Fatalf("expected output to mention --node-selector, got:\n%s", out)
	}
	if !strings.Contains(string(out), wantInvalidNodeSelectorSubstring) {
		t.Fatalf("expected output to contain %q, got:\n%s", wantInvalidNodeSelectorSubstring, out)
	}
}
