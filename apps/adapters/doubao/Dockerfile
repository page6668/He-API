# syntax=docker/dockerfile:1
# Multi-stage build for the Doubao (Volcengine Ark) adapter (Story 4.5).
#
# Builder stage: vendored Go modules + connect-go.
# Runtime stage: distroless static so the image footprint matches the
# Story-3.1 api-gateway + Story-4.1/4.2/4.3/4.4 adapter Dockerfile pattern.
#
# Cold-start budget per Architect Round 2 M5 ruling (Story 4.1, inherited
# by Stories 4.2-4.6): ≤ 2s. K8s readiness probe gates traffic until the
# Connect-RPC handler is registered.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/adapter-doubao ./apps/adapters/doubao/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/adapter-doubao /adapter-doubao
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/adapter-doubao"]
