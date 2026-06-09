# syntax=docker/dockerfile:1
# fx-refresh — He-API Story 7.2 AC2 CronJob binary (schedule `0 0 * * *` UTC).
# Dedicated `main` package (Architect H-1 — NOT a billing-svc server subcommand;
# mirrors apps/auth-svc/cmd/monthly-cost-reset). Multi-stage build + distroless
# static runtime so the cron pod carries NO HTTP-server stack. Build context is
# the repo root (the module is part of the go.work workspace).
#
#   docker build -f apps/billing-svc/cmd/fx-refresh/Dockerfile -t fx-refresh .
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/fx-refresh ./apps/billing-svc/cmd/fx-refresh

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/fx-refresh /fx-refresh
USER nonroot:nonroot
ENTRYPOINT ["/fx-refresh"]
