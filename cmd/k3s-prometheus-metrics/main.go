// Command k3s-prometheus-metrics watches Node objects on a k3s cluster and
// creates/updates discovery.k8s.io/v1 EndpointSlice objects (and,
// optionally, legacy v1 Endpoints) pointing at the kube-scheduler,
// kube-proxy, and kube-controller-manager metrics ports on each
// control-plane node, so an in-cluster Prometheus can scrape them.
//
// See https://github.com/k3s-io/k3s/issues/3619 for why this exists as a
// standalone controller rather than a feature of k3s itself.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/zakame/k3s-prometheus-metrics/internal/config"
	"github.com/zakame/k3s-prometheus-metrics/internal/controller"
)

var runtimeScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(scheme.AddToScheme(runtimeScheme))
	utilruntime.Must(discoveryv1.AddToScheme(runtimeScheme))
}

func main() {
	var (
		namespace            string
		nodeSelectorFlag     string
		writeLegacyEndpoints bool
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
	)

	flag.StringVar(&namespace, "namespace", "kube-system",
		"Namespace to create/update EndpointSlice (and, if enabled, Endpoints) objects in. "+
			"This is independent of the namespace the controller itself is deployed in.")
	flag.StringVar(&nodeSelectorFlag, "node-selector", config.ControlPlaneNodeSelector+"=true",
		"Comma-separated key=value node label selector identifying control-plane nodes. "+
			"k3s sets this label with value \"true\"; a generic kubeadm cluster would use an empty value instead.")
	flag.BoolVar(&writeLegacyEndpoints, "write-legacy-endpoints", false,
		"Also create/update legacy v1 Endpoints objects, for Kubernetes clusters older than 1.33.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"Address the controller's own Prometheus metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"Address the controller's health/readiness probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Enabling this ensures there is only one active controller manager.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("setup")

	nodeSelector, err := parseSelector(nodeSelectorFlag)
	if err != nil {
		logger.Error(err, "invalid --node-selector")
		os.Exit(1)
	}

	cfg := config.Config{
		Namespace:            namespace,
		NodeSelector:         nodeSelector,
		WriteLegacyEndpoints: writeLegacyEndpoints,
		Services:             config.DefaultServices,
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 runtimeScheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "k3s-prometheus-metrics.zakame.github.io",
		// Scope EndpointSlice to match role-endpoints.yaml's namespaced
		// RBAC; the default cache watches every type cluster-wide.
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&discoveryv1.EndpointSlice{}: {Namespaces: map[string]cache.Config{namespace: {}}},
			},
		},
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	reconciler := &controller.NodeReconciler{
		Client: mgr.GetClient(),
		Config: cfg,
	}

	if writeLegacyEndpoints {
		legacyClient, err := newLegacyEndpointsClient(mgr)
		if err != nil {
			logger.Error(err, "unable to create legacy endpoints client")
			os.Exit(1)
		}
		reconciler.LegacyClient = legacyClient
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "Node")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	logger.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// parseSelector parses a comma-separated key=value list into a label map.
// An empty string returns a nil map (no selector, i.e. all nodes match).
func parseSelector(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	sel := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("invalid selector segment %q: expected key=value", pair)
		}
		sel[k] = v
	}
	return sel, nil
}

// newLegacyEndpointsClient returns a client.Client that suppresses the v1
// Endpoints deprecation Warning header Kubernetes 1.33+ API servers attach
// to every Endpoints read/write. This client's use of that API is
// deliberate opt-in legacy support, not a bug that should be surfaced as
// log noise.
func newLegacyEndpointsClient(mgr ctrl.Manager) (client.Client, error) {
	restCfg := *mgr.GetConfig()
	restCfg.WarningHandlerWithContext = rest.NoWarnings{}
	return client.New(&restCfg, client.Options{Scheme: mgr.GetScheme()})
}
