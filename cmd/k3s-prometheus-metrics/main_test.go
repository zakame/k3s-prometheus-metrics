package main

import (
	"os/exec"
	"path/filepath"
	"reflect"
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
