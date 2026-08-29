# Architecture

This is a reference for contributors on how the codebase is organized. If
you're an operator looking to deploy or use the controller, see the main
[README.md](../README.md) instead: this document assumes you're reading
or changing the code.

The controller is built on
[controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
and organized as:

- `cmd/k3s-prometheus-metrics/`: entrypoint, manager wiring, leader
  election, and CLI flags
- `internal/controller/`: the Node watcher/reconciler. Reacts to Node
  add/update/delete events and readiness changes, and drives Service,
  EndpointSlice, and (optionally) Endpoints objects to match current
  control-plane node state, setting a controller `ownerReference` from each
  EndpointSlice/Endpoints back to its Service. An `ownerReference` is
  Kubernetes's built-in parent/child link for garbage collection: when the
  owner (the Service) is deleted, the API server automatically deletes
  everything that points back to it, so deleting the Service is enough to
  clean up the EndpointSlice/Endpoints objects too, with nothing left
  behind.
- `internal/endpoints/`: pure, unit-testable builder functions that turn
  `internal/config`'s service table into selector-less Service objects, and
  a set of control-plane nodes into matching `discovery.k8s.io/v1`
  EndpointSlice objects (and, optionally, legacy `v1` Endpoints). Nodes are
  split by their InternalIP's address family (IPv4 vs. IPv6), so a
  dual-stack cluster (one where nodes have both an IPv4 and an IPv6
  address) gets a separate `<service>-metrics-ipv6` EndpointSlice alongside
  the IPv4 one, since a single EndpointSlice's `AddressType` can't mix
  families.
- `internal/config/`: the static table of watched services and their
  metrics ports (kube-scheduler, kube-proxy, kube-controller-manager).
  `--node-selector` only narrows kube-scheduler and kube-controller-manager
  to control-plane nodes; kube-proxy's `NodeSelector` is hardcoded empty in
  this table (not flag-configurable) since kube-proxy runs on every node,
  not just control-plane ones
- `deploy/standard/`: sample manifests (namespace, RBAC, ServiceAccount,
  Deployment, ServiceMonitor, kustomization) for deploying the controller
  alongside a kube-prometheus/kube-prometheus-stack install
- `deploy/e2e/`, `deploy/e2e-legacy/`: kustomize overlays of
  `deploy/standard/` used by CI's e2e suite against a k3d cluster;
  `deploy/e2e-legacy/` additionally sets `--write-legacy-endpoints`, for the
  legacy `v1` Endpoints leg. Not intended for end users.
- `test/integration/`: envtest-backed tests that exercise the reconciler
  and RBAC manifests against a real (if ephemeral) `kube-apiserver`,
  complementing the unit tests under `internal/`. Run with `make
  test-integration`.
- `test/e2e/`: build-tag-`e2e` smoke tests that exercise the running
  controller Deployment (not envtest, not direct `Reconcile()` calls) on a
  real cluster, polling for the Service/EndpointSlice (and, with
  `E2E_LEGACY_ENDPOINTS=true`, legacy Endpoints) objects it converges to.
  Requires a cluster with `deploy/e2e` (or `deploy/e2e-legacy`) already
  applied and a kubeconfig pointed at it; run with `go test -tags e2e
  ./test/e2e/...`. CI runs this against a k3d cluster, since k3d runs real
  k3s and labels control-plane nodes the way this controller expects
  (kind/kubeadm clusters label them differently and would match zero nodes
  against the default `--node-selector`), as a two-leg matrix: one against a
  current k3s version applying `deploy/e2e` (default EndpointSlice-only
  path), one against a pre-1.33 k3s version applying `deploy/e2e-legacy`
  with `E2E_LEGACY_ENDPOINTS=true` (legacy Endpoints path).

For how the Service/EndpointSlice/Endpoints objects this reconciler creates
relate to each other and to Prometheus, see ["Where the
Service/EndpointSlice/Endpoints objects
live"](../README.md#where-the-serviceendpointsliceendpoints-objects-live)
in the README, including the diagram there.
