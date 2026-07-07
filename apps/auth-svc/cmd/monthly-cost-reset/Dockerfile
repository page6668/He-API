# syntax=docker/dockerfile:1.7
#
# monthly-cost-reset — He-API Story 5.4 AC3 CronJob binary. Multi-stage build +
# distroless final image; Pod Security Restricted profile (UID 65532). Build
# context is apps/auth-svc (mirrors the auth-svc server Dockerfile): the cron
# reuses the auth-svc repository + redisclient + audit packages.
#
#   docker build -f cmd/monthly-cost-reset/Dockerfile -t monthly-cost-reset .

FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/monthly-cost-reset ./apps/auth-svc/cmd/monthly-cost-reset

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/monthly-cost-reset /monthly-cost-reset
USER 65532
ENTRYPOINT ["/monthly-cost-reset"]
