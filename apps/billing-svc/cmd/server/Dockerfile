# syntax=docker/dockerfile:1
# Multi-stage build for billing-svc — the He-API billing engine (Epic 7, Story
# 7.1). Template inherited from apps/routing-svc/cmd/server/Dockerfile (Story
# 6.1) — distroless static runtime so the image footprint matches the gateway +
# routing-svc pattern.
#
# Cold-start budget: <= 2s. The K8s readiness probe (GET /ready) gates traffic
# until the pricing snapshot is loaded + the HTTP listener is up.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/billing-svc ./apps/billing-svc/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/billing-svc /billing-svc
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/billing-svc"]
