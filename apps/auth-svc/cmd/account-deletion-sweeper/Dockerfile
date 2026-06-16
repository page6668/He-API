# syntax=docker/dockerfile:1.7
#
# account-deletion-sweeper — He-API Story 2.7 AC6 CronJob binary. Physical-erasure
# pass after the 30-day grace. Multi-stage build + distroless final image; Pod
# Security Restricted profile (UID 65532). Build context is apps/auth-svc (mirrors
# the auth-svc server + monthly-cost-reset Dockerfiles): the sweeper reuses the
# auth-svc repository + redisclient + audit packages.
#
#   docker build -f cmd/account-deletion-sweeper/Dockerfile -t account-deletion-sweeper .
#
# Story 1.7 B6 — wired into build-images.yml (context: apps/auth-svc) so the cron
# image is produced by the same matrix that builds the deployable services.

FROM golang:1.22-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/account-deletion-sweeper ./cmd/account-deletion-sweeper

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/account-deletion-sweeper /account-deletion-sweeper
USER 65532
ENTRYPOINT ["/account-deletion-sweeper"]
