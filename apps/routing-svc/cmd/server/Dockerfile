# syntax=docker/dockerfile:1
# Multi-stage build for routing-svc — the He-API intelligent-routing service
# (Epic 6, Story 6.1). Template inherited from apps/adapters/ernie/Dockerfile
# (Story 4.6) — distroless static runtime so the image footprint matches the
# api-gateway + Epic-4 adapter pattern.
#
# Cold-start budget (Story-4.1 M5 inheritance): <= 2s. The K8s readiness probe
# (GET /ready) gates traffic until the catalogue snapshot is loaded + the
# decision engine is constructed (Story 6.1 AC1 / FLOW-001).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/routing-svc ./apps/routing-svc/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/routing-svc /routing-svc
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/routing-svc"]
