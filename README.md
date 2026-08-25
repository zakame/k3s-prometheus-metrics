### k3s-prometheus-metrics - expose k3s control-plane metrics to Prometheus

This is a Kubernetes controller that watches Node objects on a
[k3s](https://k3s.io/) cluster and creates/updates
[`discovery.k8s.io/v1` EndpointSlice](https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/)
objects (and optionally legacy `v1` Endpoints, for Kubernetes older than
1.33) pointing at the kube-scheduler, kube-proxy, and
kube-controller-manager metrics ports on each control-plane node. That lets
an in-cluster Prometheus (via [kube-prometheus] jsonnet or the
[kube-prometheus-stack] Helm chart) scrape control-plane metrics the same
way it would on a normal upstream Kubernetes cluster.

[kube-prometheus]: https://github.com/prometheus-operator/kube-prometheus
[kube-prometheus-stack]: https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack

## Background

Unlike upstream Kubernetes, k3s does not ship Services/EndpointSlices for
its bundled control-plane components' metrics endpoints, and the k3s
maintainers have declined to add this to k3s's own EndpointSlices
controller by default. See
[k3s-io/k3s#3619](https://github.com/k3s-io/k3s/issues/3619). k3s also
binds these control-plane metrics ports to loopback by default, so a
cluster admin must pass extra flags to `k3s server`/`k3s agent` to bind
them to `0.0.0.0` before anything running elsewhere in the cluster (such as
Prometheus) can reach them.

This project exists for admins who have already opted into exposing those
metrics ports and want them discovered and scraped the same way upstream
Kubernetes control-plane metrics normally are, without waiting on upstream
k3s support.

### Enabling the metrics ports on k3s

k3s has no dedicated "expose metrics" flag. Bind addresses are set via its
generic passthrough flags to the underlying components. On each server
node:

```bash
k3s server \
  --kube-scheduler-arg=bind-address=0.0.0.0 \
  --kube-controller-manager-arg=bind-address=0.0.0.0 \
  --kube-proxy-arg=metrics-bind-address=0.0.0.0:10249
```

(Add `--kube-proxy-arg=healthz-bind-address=0.0.0.0:10256` too if you also
want kube-proxy's healthz endpoint reachable. This controller does not
manage that port.)

| Component | Port | Scheme |
|-----------|------|--------|
| kube-scheduler | 10259 | https |
| kube-controller-manager | 10257 | https |
| kube-proxy | 10249 | http |

These ports are stable and version-independent. The older insecure ports
(10251/10252) were removed upstream in Kubernetes 1.22/1.23, so this
controller does not fall back to them.

##### Features:

- Watches Node objects and tracks control-plane readiness automatically.
  No static target lists to maintain as nodes join, leave, or change role.
- Manages `discovery.k8s.io/v1` EndpointSlice objects for kube-scheduler,
  kube-proxy, and kube-controller-manager metrics ports
- Optional legacy `v1` Endpoints output for clusters running Kubernetes
  older than 1.33
- Designed to be scraped like a normal upstream Kubernetes control plane.
  No custom relabeling required in kube-prometheus/kube-prometheus-stack.

## Architecture

The controller is built on [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
and organized as:

- `cmd/k3s-prometheus-metrics/`: entrypoint, manager wiring, leader
  election, and CLI flags
- `internal/controller/`: the Node watcher/reconciler. Reacts to Node
  add/update/delete events and readiness changes, and drives
  EndpointSlice/Endpoints objects to match current control-plane node state.
- `internal/endpoints/`: pure, unit-testable builder functions that turn a
  set of control-plane nodes into `discovery.k8s.io/v1` EndpointSlice
  objects (and, optionally, legacy `v1` Endpoints). Nodes are split by
  their InternalIP's address family, so a dual-stack cluster gets a
  separate `<service>-metrics-ipv6` EndpointSlice alongside the IPv4 one,
  since a single EndpointSlice's `AddressType` can't mix families.
- `internal/config/`: the static table of watched services and their
  metrics ports (kube-scheduler, kube-proxy, kube-controller-manager)
- `deploy/standard/`: sample manifests (namespace, RBAC, ServiceAccount,
  Deployment, ServiceMonitor, kustomization) for deploying the controller
  alongside a kube-prometheus/kube-prometheus-stack install

### Where the EndpointSlice/Endpoints objects live

The controller Deployment itself runs wherever you place it (e.g. a
`monitoring` namespace), but the EndpointSlice/Endpoints objects it manages
are created in **`kube-system`**, not the controller's own namespace. That
matches upstream kubeadm clusters, where kube-prometheus and
kube-prometheus-stack's bundled ServiceMonitors already expect to find
`kube-scheduler`, `kube-proxy`, and `kube-controller-manager` Services in
`kube-system`. The sample manifests in `deploy/standard/` include static
(selector-less) Service objects for those three names in `kube-system`,
paired with this controller's dynamically-managed EndpointSlices, so
existing kube-prometheus/kube-prometheus-stack ServiceMonitors pick up the
targets with no relabeling changes.

## Installation

### Using a Container Image

Pull the container image from GitHub Container Registry:

```bash
docker pull ghcr.io/zakame/k3s-prometheus-metrics:master
```

(Tagged releases publish a `vX.Y.Z` image tag in addition to `master`; see
the [releases page](https://github.com/zakame/k3s-prometheus-metrics/releases).)

### Building from Source

Requirements:
- Go 1.27 or later

```bash
git clone https://github.com/zakame/k3s-prometheus-metrics.git
cd k3s-prometheus-metrics
make build
./bin/k3s-prometheus-metrics
```

Running it locally like this uses your current kubeconfig context. That is
useful for testing against a real cluster before deploying in-cluster.

## Usage

The controller needs a `ServiceAccount` with permission to list/watch
Nodes and create/update EndpointSlices (and, if `--write-legacy-endpoints`
is set, Endpoints). See [Kubernetes Deployment](#kubernetes-deployment)
below for the RBAC this requires. Once running, it reconciles continuously.
No scheduling flag is needed to make it re-check node state periodically,
since it watches Node objects directly.

### CLI Flags

| Flag | Default | Description |
|------|---------|--------------|
| `--namespace` | `kube-system` | Namespace to create/update EndpointSlice (and, if enabled, Endpoints) objects in. Independent of the namespace the controller itself is deployed in. |
| `--node-selector` | `node-role.kubernetes.io/control-plane=true` | Comma-separated `key=value` node label selector identifying control-plane nodes. k3s sets this label to `true`; a kubeadm cluster would use an empty value instead. |
| `--write-legacy-endpoints` | `false` | Also create/update legacy `v1` Endpoints objects, for Kubernetes clusters older than 1.33. |
| `--metrics-bind-address` | `:8080` | Address the controller's own Prometheus metrics endpoint binds to. |
| `--health-probe-bind-address` | `:8081` | Address the controller's `/healthz` and `/readyz` probe endpoint binds to. |
| `--leader-elect` | `false` | Enable leader election, so only one replica is active at a time. |

The controller also accepts the standard controller-runtime zap logging
flags (`-zap-devel`, `-zap-encoder`, `-zap-log-level`, `-zap-stacktrace-level`,
`-zap-time-encoding`).

## Kubernetes Deployment

Sample manifests are available in [`deploy/standard/`](deploy/standard/):

- `namespace.yaml`: a `monitoring` namespace for the controller Deployment
  (idempotent if you already have one, e.g. from kube-prometheus-stack)
- `serviceaccount.yaml`, `role*.yaml`, `rolebinding*.yaml`: the
  `ServiceAccount` and least-privilege RBAC the controller needs (see below)
- `deployment.yaml`: the controller Deployment itself, scheduled onto
  control-plane nodes with `--leader-elect` enabled
- `control-plane-services.yaml`: static, selector-less `Service` objects
  for `kube-scheduler`, `kube-controller-manager`, and `kube-proxy` in
  `kube-system`, so the controller's EndpointSlices have a Service to back
- `servicemonitor.yaml`: `ServiceMonitor` objects wiring those Services
  into a Prometheus Operator setup (see below)
- `kustomization.yaml`: ties the above together

```bash
kubectl apply -k deploy/standard/
```

### Port names and existing kube-prometheus ServiceMonitors

The `kube-scheduler` and `kube-controller-manager` Services/EndpointSlices
use the port name `https-metrics` and the `kube-proxy` one uses
`http-metrics` (set in `internal/config.DefaultServices`). The
`https-metrics` name is not arbitrary. It matches
[kube-prometheus]'s own stock
`kubernetesControlPlane-serviceMonitorKube{Scheduler,ControllerManager}.yaml`
ServiceMonitors exactly (same port name, bearer-token auth, skip-verify TLS,
and separate `/metrics/slis` scrape). **If your cluster already runs
kube-prometheus's bundled control-plane ServiceMonitors, you likely don't
need `servicemonitor.yaml`'s kube-scheduler/kube-controller-manager entries
at all.** Just the Service and EndpointSlice from
`control-plane-services.yaml` are enough for kube-prometheus's existing
ServiceMonitors to start matching. `deploy/standard/servicemonitor.yaml`
provides equivalent ServiceMonitors for kube-prometheus-stack users, or
anyone not already running kube-prometheus's stock ones.

kube-proxy has **no upstream kube-prometheus ServiceMonitor at all**. Its
metrics port is unauthenticated plain HTTP rather than HTTPS with delegated
authz, so it doesn't fit the scheduler/controller-manager pattern, and
kube-prometheus drops it for the same host-network/loopback-bind story this
project exists to work around. The `http-metrics` port name and its
ServiceMonitor entry are this project's own convention.

### Prerequisite: Prometheus RBAC for secured metrics endpoints

Scraping the kube-scheduler and kube-controller-manager `https-metrics`
endpoints requires the scraping Prometheus's own `ServiceAccount` to be
authorized for `nonResourceURLs: ["/metrics", "/metrics/slis"]` (verb
`get`) against the Kubernetes API server's delegated authorization. This
project's manifests do not (and cannot) grant that, since it's RBAC on the
*scraper's* identity, not the controller's. Both kube-prometheus's
`prometheus-k8s` `ClusterRole` and kube-prometheus-stack's default
Prometheus `ClusterRole` already include this grant, so this is normally
already satisfied. Just be aware of it if you've stripped down Prometheus's
default RBAC.

### RBAC

RBAC is split across three role/binding pairs, each scoped to the least
access it needs rather than one broad grant:

- `role.yaml` + `rolebinding.yaml`: a `ClusterRole`/`ClusterRoleBinding`
  granting only `get`, `list`, `watch` on `nodes`. Cluster-scoped because
  Node objects have no namespace. Read-only because the controller never
  modifies Nodes themselves. `role.yaml` is generated from
  `+kubebuilder:rbac` markers via `make manifests`.
- `role-endpoints.yaml` + `rolebinding-endpoints.yaml`: a namespaced
  `Role`/`RoleBinding` **in `kube-system`** (matching the `--namespace`
  default) granting `get`, `list`, `watch`, `create`, `update`, `patch` on
  `discovery.k8s.io` `endpointslices` and core `endpoints`. If you change
  `--namespace`, this Role and RoleBinding must move to that namespace too.
  Hand-maintained rather than generated, since controller-gen only
  produces one ClusterRole from all markers and can't express "cluster-wide
  read, namespace-scoped write" as separate namespaced roles.
- `role-leader-election.yaml` + `rolebinding-leader-election.yaml`: a
  namespaced `Role`/`RoleBinding` **in `monitoring`** (the controller's own
  namespace) granting access to `coordination.k8s.io` `leases`, needed
  because `--leader-elect` coordinates via a Lease in the pod's own
  namespace.

The net effect: a compromised controller pod can read Nodes cluster-wide,
but can only create/modify EndpointSlices, Endpoints, or Leases in the two
specific namespaces it actually needs. Not cluster-wide, and not in any
other namespace.

### Verifying Prometheus is scraping the new targets

Once the controller is running and the static Services in
`control-plane-services.yaml` are applied:

```bash
kubectl get endpointslices -n kube-system -l endpointslice.kubernetes.io/managed-by=k3s-prometheus-metrics
```

should list one EndpointSlice per service (`kube-scheduler-metrics`,
`kube-controller-manager-metrics`, `kube-proxy-metrics`), each with one
endpoint address per matching control-plane node. From there, check
Prometheus's own **Status → Targets** page for the `kube-scheduler`,
`kube-controller-manager`, and `kube-proxy` jobs to confirm scrapes are
succeeding. A `context deadline exceeded` or `403`-style scrape error there
most likely means the metrics port isn't actually bound to a non-loopback
address yet (see [Enabling the metrics ports on
k3s](#enabling-the-metrics-ports-on-k3s)), or the Prometheus RBAC
prerequisite above isn't satisfied.

## License

See [LICENSE](LICENSE) file for details.
