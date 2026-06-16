# syntax=docker/dockerfile:1.7
#
# safety-log-retention — He-API Story 8.5 AC2 CronJob binary. Deletes
# he_api.content_safety_logs rows older than 6 months (§9.3 治理日志 retention).
# Multi-stage build + distroless final image; Pod Security Restricted profile
# (UID 65532). Build context is apps/api-gateway (mirrors the api-gateway server
# Dockerfile): the cron reuses the api-gateway repository + db packages.
#
#   docker build -f cmd/safety-log-retention/Dockerfile -t safety-log-retention .
#
# Story 1.7 B6 — wired into build-images.yml (context: apps/api-gateway) so the
# cron image is produced by the same matrix that builds the deployable services.

FROM golang:1.22-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/safety-log-retention ./cmd/safety-log-retention

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/safety-log-retention /safety-log-retention
USER 65532
ENTRYPOINT ["/safety-log-retention"]
