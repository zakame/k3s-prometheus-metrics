# Local/dev convenience image (`make docker-build`).
# Tagged releases and master-branch images are built by goreleaser via ko
# (see .goreleaser.yaml / .goreleaser.master.yaml), not this file.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/k3s-prometheus-metrics ./cmd/k3s-prometheus-metrics

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/k3s-prometheus-metrics /k3s-prometheus-metrics
USER 65532:65532
ENTRYPOINT ["/k3s-prometheus-metrics"]
