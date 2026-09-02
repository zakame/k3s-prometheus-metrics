package main

import (
	"os/exec"
	"strings"
	"testing"
)

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
