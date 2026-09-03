package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// TestKubeconfigEnvVar_TakesPriorityOverNativeKUBECONFIG guards that
// K3S_PROMETHEUS_METRICS_KUBECONFIG, once it pre-seeds --kubeconfig via
// applyEnvDefaults, wins over client-go's own KUBECONFIG env var --
// consistent with GetConfig's documented precedence (--kubeconfig flag >
// KUBECONFIG env var > in-cluster > $HOME/.kube/config), since pre-seeding
// the flag makes GetConfig see it as already set. Both env vars point at
// nonexistent files, so the process fails either way -- this checks WHICH
// path it tried, via the error text, not whether it succeeds.
func TestKubeconfigEnvVar_TakesPriorityOverNativeKUBECONFIG(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"K3S_PROMETHEUS_METRICS_KUBECONFIG=/nonexistent/from-k3s-prometheus-metrics-env",
		"KUBECONFIG=/nonexistent/from-native-kubeconfig-env",
	)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if !strings.Contains(string(out), "from-k3s-prometheus-metrics-env") {
		t.Fatalf("expected the env-seeded --kubeconfig path in the error, got:\n%s", out)
	}
	if strings.Contains(string(out), "from-native-kubeconfig-env") {
		t.Fatalf("expected the native KUBECONFIG path NOT to be used once --kubeconfig is env-seeded, got:\n%s", out)
	}
}

// TestKubeconfigFlag_ExitsNonZeroForNonexistentFile guards that an explicit
// --kubeconfig CLI flag (not just the env-var fallback) actually reaches
// GetConfigOrDie for the controller entrypoint: prior coverage only checked
// that -h output mentions the flag, never that a real value took effect.
func TestKubeconfigFlag_ExitsNonZeroForNonexistentFile(t *testing.T) {
	bin := buildBinary(t)

	out, err := exec.Command(bin, "-kubeconfig=/nonexistent/from-cli-flag-only").CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if !strings.Contains(string(out), "from-cli-flag-only") {
		t.Fatalf("expected the --kubeconfig path in the error, got:\n%s", out)
	}
}

// TestKubeconfigFlag_TakesPriorityOverEnvVar guards that an explicit
// --kubeconfig CLI flag wins over K3S_PROMETHEUS_METRICS_KUBECONFIG, the same
// precedence env_flags_test.go already checks for --namespace, but not yet
// for --kubeconfig specifically -- the flag most likely to matter for
// cluster safety. Both point at nonexistent files, so the process fails
// either way; this checks WHICH path it tried, via the error text.
func TestKubeconfigFlag_TakesPriorityOverEnvVar(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "-kubeconfig=/nonexistent/from-cli-flag")
	cmd.Env = append(os.Environ(), "K3S_PROMETHEUS_METRICS_KUBECONFIG=/nonexistent/from-env-var")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if !strings.Contains(string(out), "from-cli-flag") {
		t.Fatalf("expected the explicit --kubeconfig CLI flag to win, got:\n%s", out)
	}
	if strings.Contains(string(out), "from-env-var") {
		t.Fatalf("expected the env-var path NOT to be used once --kubeconfig is set explicitly, got:\n%s", out)
	}
}

// TestRunManifests_KubeconfigFlag_ExitsNonZeroForNonexistentFile is the
// manifests-subcommand counterpart of
// TestKubeconfigFlag_ExitsNonZeroForNonexistentFile: --kubeconfig is
// registered on manifests' own FlagSet via ctrl.RegisterFlags, a different
// code path than the controller's package-level flag.CommandLine
// registration, so it needs its own end-to-end check.
//
// Unlike the controller entrypoint, runManifests never calls
// ctrl.SetLogger, so GetConfigOrDie's internal error log goes to
// controller-runtime's default NullLogSink and never reaches stderr --
// the process exits 1 with no diagnostic at all. This test pins that
// (arguably poor) actual behavior rather than an error string that doesn't
// exist, so a regression either direction (e.g. no longer failing, or
// gaining/losing the silent-exit quirk) gets caught.
func TestRunManifests_KubeconfigFlag_ExitsNonZeroForNonexistentFile(t *testing.T) {
	bin := buildBinary(t)

	out, err := exec.Command(bin, "manifests", "-kubeconfig=/nonexistent/from-cli-flag-only").CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if len(out) != 0 {
		t.Fatalf("expected no output (runManifests never calls ctrl.SetLogger, so GetConfigOrDie's error log is discarded), got:\n%s", out)
	}
}

// TestRunManifests_KubeconfigEnvVar_ExitsNonZeroSilently guards that
// K3S_PROMETHEUS_METRICS_KUBECONFIG reaches manifests' own FlagSet (via
// applyEnvDefaults(fs) in runManifests, not the top-level flag.CommandLine
// applyEnvDefaults tested in TestKubeconfigEnvVar_TakesPriorityOverNativeKUBECONFIG),
// exercised through the actual "manifests" subcommand rather than just the
// top-level binary. See the comment on
// TestRunManifests_KubeconfigFlag_ExitsNonZeroForNonexistentFile for why
// there's no error text to assert on here.
func TestRunManifests_KubeconfigEnvVar_ExitsNonZeroSilently(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "manifests")
	cmd.Env = append(os.Environ(), "K3S_PROMETHEUS_METRICS_KUBECONFIG=/nonexistent/from-env-var-manifests")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if len(out) != 0 {
		t.Fatalf("expected no output (runManifests never calls ctrl.SetLogger, so GetConfigOrDie's error log is discarded), got:\n%s", out)
	}
}

// TestZapDevelEnvVar_ReachesZapBoundFlag guards that applyEnvDefaults runs
// after zap.Options.BindFlags registers its flags, not before -- otherwise
// the env-var fallback would silently skip every zap flag. --zap-devel
// switches the log encoder from JSON to console, so its effect is
// observable in output shape rather than a flag value.
func TestZapDevelEnvVar_ReachesZapBoundFlag(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"K3S_PROMETHEUS_METRICS_ZAP_DEVEL=true",
		"K3S_PROMETHEUS_METRICS_KUBECONFIG=/nonexistent/zap-devel-probe",
	)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected a non-zero exit code, got success; output:\n%s", out)
	}
	if strings.Contains(string(out), `"level":"error"`) {
		t.Fatalf("expected console encoding (not JSON) once -zap-devel is env-seeded, got:\n%s", out)
	}
	if !strings.Contains(string(out), "ERROR") {
		t.Fatalf("expected a console-encoded ERROR line, got:\n%s", out)
	}
}

// flagNameRe extracts registered flag names from -h/-help output, as
// flag.PrintDefaults prints them: "  -name" or "  -name type".
var flagNameRe = regexp.MustCompile(`(?m)^  -([A-Za-z0-9-]+)`)

func flagNamesFromUsage(usage string) []string {
	var names []string
	for _, m := range flagNameRe.FindAllStringSubmatch(usage, -1) {
		names = append(names, m[1])
	}
	return names
}

// TestEnvVarNames_NoCollisionAcrossRegisteredFlags guards envPrefix's
// name-derivation (hyphens to underscores, upper-cased) against two
// different flag names collapsing onto the same env var name -- possible
// if a future flag name contained a literal underscore, or two flag names
// differed only by case. Checked against the actual shipped flag set for
// both entrypoints (via -h output), not a hand-copied list, so it can't
// drift out of sync with what's really registered.
func TestEnvVarNames_NoCollisionAcrossRegisteredFlags(t *testing.T) {
	bin := buildBinary(t)

	for _, args := range [][]string{{"-h"}, {"manifests", "-h"}} {
		out, _ := exec.Command(bin, args...).CombinedOutput()
		seen := map[string]string{}
		for _, name := range flagNamesFromUsage(string(out)) {
			envName := envPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
			if prev, ok := seen[envName]; ok && prev != name {
				t.Errorf("%v: flags %q and %q both map to env var %s", args, prev, name, envName)
			}
			seen[envName] = name
		}
		if len(seen) == 0 {
			t.Fatalf("%v: no flags parsed from usage output:\n%s", args, out)
		}
	}
}
