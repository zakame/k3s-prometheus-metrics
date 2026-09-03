package main

import (
	"flag"
	"testing"
)

func newTestFlagSet() (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var namespace string
	var writeLegacy bool
	fs.StringVar(&namespace, "namespace", "kube-system", "")
	fs.BoolVar(&writeLegacy, "write-legacy-endpoints", false, "")
	return fs, &namespace, &writeLegacy
}

func TestApplyEnvDefaults_SetsUnsetFlagFromEnv(t *testing.T) {
	fs, namespace, _ := newTestFlagSet()
	t.Setenv("K3S_PROMETHEUS_METRICS_NAMESPACE", "from-env")

	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if *namespace != "from-env" {
		t.Errorf("namespace = %q, want %q", *namespace, "from-env")
	}
}

func TestApplyEnvDefaults_ExplicitFlagOverridesEnv(t *testing.T) {
	fs, namespace, _ := newTestFlagSet()
	t.Setenv("K3S_PROMETHEUS_METRICS_NAMESPACE", "from-env")

	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if err := fs.Parse([]string{"-namespace=from-cli"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if *namespace != "from-cli" {
		t.Errorf("namespace = %q, want %q (explicit flag should win over env)", *namespace, "from-cli")
	}
}

func TestApplyEnvDefaults_NoEnvVarLeavesDefault(t *testing.T) {
	fs, namespace, _ := newTestFlagSet()

	if err := applyEnvDefaults(fs); err != nil {
		t.Fatalf("applyEnvDefaults: %v", err)
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if *namespace != "kube-system" {
		t.Errorf("namespace = %q, want unchanged default %q", *namespace, "kube-system")
	}
}

func TestApplyEnvDefaults_InvalidValueReturnsError(t *testing.T) {
	fs, _, _ := newTestFlagSet()
	t.Setenv("K3S_PROMETHEUS_METRICS_WRITE_LEGACY_ENDPOINTS", "notabool")

	err := applyEnvDefaults(fs)
	if err == nil {
		t.Fatal("expected an error for an unparseable bool env value, got nil")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected a non-empty error message naming the offending env var")
	}
}
