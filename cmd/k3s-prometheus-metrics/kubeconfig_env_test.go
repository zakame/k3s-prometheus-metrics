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
