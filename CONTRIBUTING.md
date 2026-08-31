# Contributing

Thanks for your interest in improving k3s-prometheus-metrics.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the package layout
and how the test suites are organized before making changes.

## Commit messages: Scoped Commits

This repository follows the [Scoped Commits](https://scopedcommits.com/)
convention instead of Conventional Commits. Every commit subject has the form:

```text
<scope>: <description>
```

- **scope** is the path to the file, directory, or subsystem the commit
  changes. It is not a change-type keyword like `feat` or `fix`.
- **description** is a short, imperative summary of what changed in that
  scope.

A commit that touches more than one unrelated scope should usually be split
into multiple commits, one per scope.

### Examples

```text
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
- If you touched the reconciler, `deploy/e2e/`, `deploy/e2e-legacy/`, or
  `test/e2e/` itself, run `make test-e2e` against a real cluster: plain
  `make test-e2e` against a cluster with `deploy/e2e` applied covers the
  default EndpointSlice-only path; `E2E_LEGACY_ENDPOINTS=true make
  test-e2e` against a cluster with `deploy/e2e-legacy` applied covers the
  legacy `v1` Endpoints path too (that test skips harmlessly without the
  env var set). CI runs both automatically; neither is part of `make
  test`/`make test-integration`.
- Validate manifests build cleanly, e.g.
  `go run sigs.k8s.io/kustomize/kustomize/v5 build deploy/standard | go run github.com/yannh/kubeconform/cmd/kubeconform -summary -ignore-missing-schemas`
  (and the same for `deploy/dev`, `deploy/e2e`, and `deploy/e2e-legacy`) if
  you touched anything under `deploy/`.
- `make docs-lint` (formatting, spelling, and Mermaid diagram syntax) if you
  touched any Markdown file; needs Node.js for `npx` to run
  markdownlint-cli2 and mermaid-cli. PRs that only touch Markdown files
  skip the checks above and run this instead. CI runs this on Node.js
  24.20.0 (see `.github/workflows/docs.yaml`); use the same version
  locally if you hit lint results that don't match CI.

Also:

- Keep commits scoped as above; do not squash unrelated changes together.
- Update relevant documentation (`README.md`, manifests under `deploy/`,
  etc.) in the same pull request as the code change it describes.

## Releasing

Pushing a tag triggers `.github/workflows/release.yaml`, which runs
[GoReleaser](https://goreleaser.com/) (`goreleaser release --clean`) per
`.goreleaser.yaml`. GoReleaser builds the binaries, publishes the container
image via [`ko`](https://ko.build/), and generates the GitHub release notes.

The changelog is grouped and filtered from `git log` using the
`.goreleaser.yaml` `changelog` config, matched against commit **scopes**
(the Scoped Commits convention above), not commit type keywords:

- Commits scoped under `deploy/` go in a "Deployment" group; everything
  else goes in "Changes".
- Commits scoped under `test/`, `.github/`, `.goreleaser`, `Dockerfile:`,
  `Makefile:`, `README`, `CONTRIBUTING`, or `docs/` are excluded from the
  changelog entirely.

This is another reason to keep commits properly scoped: an unscoped or
mis-scoped commit subject doesn't just make `git log` harder to read, it
also changes which changelog group (or exclusion) that commit lands in.

## Code of conduct

Be respectful and constructive in issues, pull requests, and reviews.
