# syntax=docker/dockerfile:1
# Multi-stage build for payment-svc — the He-API payment-channel integration
# service (Epic 7, Story 7.3). Template inherited from apps/billing-svc/cmd/
# server/Dockerfile (Story 7.1) — distroless static runtime so the image
# footprint matches the gateway + billing-svc + routing-svc pattern.
#
# Cold-start budget: <= 2s. The K8s readiness probe (GET /ready) gates traffic
# until the HTTP listener is up.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/payment-svc ./apps/payment-svc/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/payment-svc /payment-svc
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/payment-svc"]
