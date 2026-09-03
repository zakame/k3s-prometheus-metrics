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

// TestApplyEnvDefaults_BoolValues pins down which env values a bool flag
// actually accepts, since fs.Set delegates to strconv.ParseBool rather than
// a hand-picked "true"/"false" check.
func TestApplyEnvDefaults_BoolValues(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		want    bool
		wantErr bool
	}{
		{name: "1 is true", val: "1", want: true},
		{name: "t is true", val: "t", want: true},
		{name: "T is true", val: "T", want: true},
		{name: "true is true", val: "true", want: true},
		{name: "TRUE is true", val: "TRUE", want: true},
		{name: "True is true", val: "True", want: true},
		{name: "0 is false", val: "0", want: false},
		{name: "f is false", val: "f", want: false},
		{name: "F is false", val: "F", want: false},
		{name: "false is false", val: "false", want: false},
		{name: "FALSE is false", val: "FALSE", want: false},
		{name: "False is false", val: "False", want: false},
		{name: "yes is invalid", val: "yes", wantErr: true},
		{name: "on is invalid", val: "on", wantErr: true},
		{name: "2 is invalid", val: "2", wantErr: true},
		{name: "empty string is invalid", val: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs, _, writeLegacy := newTestFlagSet()
			t.Setenv("K3S_PROMETHEUS_METRICS_WRITE_LEGACY_ENDPOINTS", tt.val)

			err := applyEnvDefaults(fs)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("applyEnvDefaults(%q): expected an error, got nil", tt.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyEnvDefaults(%q): unexpected error: %v", tt.val, err)
			}
			if err := fs.Parse(nil); err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if *writeLegacy != tt.want {
				t.Errorf("writeLegacyEndpoints = %v, want %v", *writeLegacy, tt.want)
			}
		})
	}
}
