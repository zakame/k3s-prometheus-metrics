package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// wantNodeSelectorDefault is k3s's node-role.kubernetes.io/control-plane
// label value; kubeadm uses an empty value instead. Hardcoded so it can't
// silently drift with config.ControlPlaneNodeSelector.
const wantNodeSelectorDefault = `node-role.kubernetes.io/control-plane=true`

// TestNodeSelectorFlagDefault_MatchesK3sConvention builds the actual binary
// and checks its own -h output, so it tests what ships, not a re-declared
// copy of the default.
func TestNodeSelectorFlagDefault_MatchesK3sConvention(t *testing.T) {
	bin := buildBinary(t)

	// -h makes flag.Parse print usage, including each flag's default.
	out, _ := exec.Command(bin, "-h").CombinedOutput()

	wantDefault := `(default "` + wantNodeSelectorDefault + `")`
	if !strings.Contains(string(out), wantDefault) {
		t.Fatalf("expected --node-selector default %s in -h output, got:\n%s", wantDefault, out)
	}
}

// TestUsage_MentionsManifestsSubcommand guards that top-level -h output
// points users at the "manifests" subcommand, so it stays discoverable
// without reading the README.
func TestUsage_MentionsManifestsSubcommand(t *testing.T) {
	bin := buildBinary(t)

	out, _ := exec.Command(bin, "-h").CombinedOutput()

	if !strings.Contains(string(out), "manifests") {
		t.Fatalf("expected top-level -h output to mention the manifests subcommand, got:\n%s", out)
	}
}

// TestManifestsUsage_DoesNotMentionTopLevelUsage guards that "manifests -h"
// uses its own flag.FlagSet, unaffected by runController's flag.Usage
// override on the global flag.CommandLine: it should print its own flags
// (e.g. -namespace) and not the top-level "Also see ... manifests -h"
// pointer, which would be a confusing self-reference.
func TestManifestsUsage_DoesNotMentionTopLevelUsage(t *testing.T) {
	bin := buildBinary(t)

	out, _ := exec.Command(bin, "manifests", "-h").CombinedOutput()

	if strings.Contains(string(out), "Also see") {
		t.Fatalf("expected 'manifests -h' output to omit the top-level usage pointer, got:\n%s", out)
	}
	if !strings.Contains(string(out), "-namespace") {
		t.Fatalf("expected 'manifests -h' output to print its own flags (e.g. -namespace), got:\n%s", out)
	}
}

// TestManifestsUsage_MentionsKubeconfigFlag guards that "manifests -h" gets
// --kubeconfig via ctrl.RegisterFlags on its own FlagSet, since it doesn't
// inherit flag.CommandLine's package-level registration the controller path
// gets for free.
func TestManifestsUsage_MentionsKubeconfigFlag(t *testing.T) {
	bin := buildBinary(t)

	out, _ := exec.Command(bin, "manifests", "-h").CombinedOutput()

	if !strings.Contains(string(out), "-kubeconfig") {
		t.Fatalf("expected 'manifests -h' output to mention -kubeconfig, got:\n%s", out)
	}
}

var (
	buildBinaryOnce sync.Once
	builtBinaryPath string
	buildBinaryErr  error
)

// buildBinary returns the path to a k3s-prometheus-metrics binary, built
// once and shared across every test in this package that needs it, so -h
// and flag-behavior tests exercise what actually ships. go build's darwin
// codesigning step costs several seconds for a brand-new output path, and
// building a fresh temp binary per test (as this used to do via
// t.TempDir()) made this package's test run take nearly a minute.
func buildBinary(t *testing.T) string {
	t.Helper()
	buildBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "k3s-prometheus-metrics-test-bin")
		if err != nil {
			buildBinaryErr = fmt.Errorf("creating temp dir for test binary: %w", err)
			return
		}
		bin := filepath.Join(dir, "k3s-prometheus-metrics")
		if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
			buildBinaryErr = fmt.Errorf("building binary: %w\n%s", err, out)
			return
		}
		builtBinaryPath = bin
	})
	if buildBinaryErr != nil {
		t.Fatalf("%v", buildBinaryErr)
	}
	return builtBinaryPath
}

// TestMain sets a logger before any test starts an envtest environment --
// otherwise controller-runtime's delegating log sink falls back to a
// NullLogSink 30s into the process and dumps a stack trace to stderr on
// the next log call. It also cleans up the shared buildBinary() output,
// if anything actually built one.
func TestMain(m *testing.M) {
	ctrl.SetLogger(logr.Discard())
	code := m.Run()
	if builtBinaryPath != "" {
		_ = os.RemoveAll(filepath.Dir(builtBinaryPath))
	}
	os.Exit(code)
}

func TestParseSelector(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    map[string]string
		wantErr bool
	}{
		{name: "empty string returns nil map", in: "", want: nil},
		{name: "single pair", in: "a=b", want: map[string]string{"a": "b"}},
		{name: "multiple pairs", in: "a=b,c=d", want: map[string]string{"a": "b", "c": "d"}},
		{name: "k3s default selector", in: wantNodeSelectorDefault, want: map[string]string{"node-role.kubernetes.io/control-plane": "true"}},
		{name: "kubeadm-style empty value", in: "node-role.kubernetes.io/control-plane=", want: map[string]string{"node-role.kubernetes.io/control-plane": ""}},
		{name: "duplicate key last one wins", in: "a=b,a=c", want: map[string]string{"a": "c"}},
		{name: "value containing equals sign", in: "a=b=c", want: map[string]string{"a": "b=c"}},
		{name: "missing equals sign errors", in: "a", wantErr: true},
		{name: "one good pair one bad segment errors", in: "a=b,c", wantErr: true},
		{name: "trailing comma yields empty segment error", in: "a=b,", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSelector(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSelector(%q): expected an error, got nil (map: %v)", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSelector(%q): unexpected error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSelector(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
