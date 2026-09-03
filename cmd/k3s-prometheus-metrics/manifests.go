// The "manifests" subcommand lists cluster Nodes once and prints the same
// Service/EndpointSlice/Endpoints objects the live controller would
// converge to, as YAML for `kubectl apply -f -`.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/controller"
	"github.com/zakame/k3s-prometheus-metrics/internal/endpoints"
	"github.com/zakame/k3s-prometheus-metrics/internal/manifest"
)

func runManifests(args []string) {
	fs := flag.NewFlagSet("manifests", flag.ExitOnError)

	cf := registerConfigFlags(fs,
		"Namespace the generated Service/EndpointSlice (and, if enabled, Endpoints) manifests target.",
		"Comma-separated key=value node label selector identifying control-plane nodes. Same meaning as the controller's --node-selector.",
		"Also emit legacy v1 Endpoints manifests, for Kubernetes clusters older than 1.33.")
	// Safe to register here even though --kubeconfig also exists on
	// flag.CommandLine for the controller path: main.go branches to exactly
	// one of the two paths per process, never both.
	ctrl.RegisterFlags(fs)
	if err := applyEnvDefaults(fs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_ = fs.Parse(args) // ExitOnError already exits on failure or -h

	ctrl.SetLogger(zap.New())

	cfg, err := cf.build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --node-selector: %v\n", err)
		os.Exit(1)
	}

	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: runtimeScheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "building Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	if err := generateManifests(context.Background(), os.Stdout, c, *cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// generateManifests lists nodes once via c, builds the same objects the
// live controller would, and writes them as YAML to w.
func generateManifests(ctx context.Context, w io.Writer, c client.Client, cfg config.Config) error {
	nodesByService, err := controller.ListNodesByService(ctx, c, cfg)
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	svcs := endpoints.BuildServices(cfg)
	slices := endpoints.BuildEndpointSlices(nodesByService, cfg)
	var eps []corev1.Endpoints //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	if cfg.WriteLegacyEndpoints {
		eps = endpoints.BuildEndpoints(nodesByService, cfg) //nolint:staticcheck
	}

	out, err := manifest.Render(svcs, slices, eps)
	if err != nil {
		return fmt.Errorf("rendering manifests: %w", err)
	}
	_, err = w.Write(out)
	return err
}
