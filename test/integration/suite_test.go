// Package integration exercises NodeReconciler against a real (envtest)
// Kubernetes API server -- no fakes, no mocks. It requires envtest binaries
// (etcd, kube-apiserver) on disk; run:
//
//	go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.1 use 1.36.2 -p path
//	KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.1 use 1.36.2 -p path)" \
//	  go test -tags integration ./test/integration/...
//
// The build tag keeps these out of `go test ./...` so the plain unit-test
// path never requires envtest binaries to be present.
//
//go:build integration

package integration

import (
	"fmt"
	"os"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	testEnv     *envtest.Environment
	k8sClient   client.Client
	adminConfig *rest.Config
	testScheme  *runtime.Scheme
)

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{}

	restCfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "starting envtest environment:", err)
		os.Exit(1)
	}
	adminConfig = restCfg

	testScheme = runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(testScheme))
	utilruntime.Must(discoveryv1.AddToScheme(testScheme))

	k8sClient, err = client.New(restCfg, client.Options{Scheme: testScheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating envtest client:", err)
		_ = testEnv.Stop()
		os.Exit(1)
	}

	code := m.Run()

	if err := testEnv.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "stopping envtest environment:", err)
	}
	os.Exit(code)
}
