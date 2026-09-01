package config_test

import (
	"testing"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
)

// TestDefaultServices_NamesAreUnique guards an assumption the reconciler
// relies on but never checks itself: duplicate names here would silently
// collapse two components into one Service (last one wins).
func TestDefaultServices_NamesAreUnique(t *testing.T) {
	seen := make(map[string]bool, len(config.DefaultServices))
	for _, svc := range config.DefaultServices {
		if seen[svc.Name] {
			t.Fatalf("duplicate Service.Name %q in DefaultServices", svc.Name)
		}
		seen[svc.Name] = true
	}
}
