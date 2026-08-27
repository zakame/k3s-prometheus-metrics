# Contributing

Thanks for your interest in improving k3s-prometheus-metrics.

## Commit messages: Scoped Commits

This repository follows the [Scoped Commits](https://scopedcommits.com/)
convention instead of Conventional Commits. Every commit subject has the form:

```
<scope>: <description>
```

- **scope** is the path to the file, directory, or subsystem the commit
  changes. It is not a change-type keyword like `feat` or `fix`.
- **description** is a short, imperative summary of what changed in that
  scope.

A commit that touches more than one unrelated scope should usually be split
into multiple commits, one per scope.

### Examples

```
internal/endpoints: derive EndpointSlice AddressType from actual address family
deploy/standard: scope RBAC to least privilege, add leader-election Role, fix image tag
Makefile: add build/test/lint/manifests/docker-build targets
```

See `git log` for the full commit history of this repository.

### Why scope instead of type

A scope tells reviewers and future readers *where* to look, which matters
more in a small controller codebase than *what kind* of change it was. `git
log -- internal/controller` becomes a meaningful changelog for that package
without needing a separate type taxonomy.

## Before opening a pull request

Run the same checks CI runs on every pull request, and make sure they pass:

- `make build`, `make vet`, `make fmt`, and `make lint` (falls back to
  `go vet` if `golangci-lint` isn't installed locally).
- `make test` (unit tests).
- `make test-integration` (envtest-backed reconciler/RBAC tests in
  `test/integration/`; downloads a pinned `kube-apiserver`/`etcd` via
  `setup-envtest` on first run).
- If you touched the reconciler, `deploy/e2e/`, or `test/e2e/` itself, run
  `make test-e2e` against a real cluster, with `deploy/e2e` already applied
  to a k3d cluster (CI does this automatically; it's not part of `make
  test`/`make test-integration`).
- Validate manifests build cleanly, e.g.
  `go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.1 build deploy/standard | go run github.com/yannh/kubeconform/cmd/kubeconform@v0.8.0 -summary -ignore-missing-schemas`
  (and the same for `deploy/dev` and `deploy/e2e`) if you touched anything
  under `deploy/`.

Also:

- Keep commits scoped as above; do not squash unrelated changes together.
- Update relevant documentation (`README.md`, manifests under `deploy/`,
  etc.) in the same pull request as the code change it describes.

## Code of conduct

Be respectful and constructive in issues, pull requests, and reviews.
