# syntax=docker/dockerfile:1.7
#
# analytics-svc — Kafka consumer for GDPR data-export jobs (Story 2.6 AC4).
# Multi-stage build + distroless final image; Pod Security Restricted profile
# (UID 65532) admits the pod into he-api-staging. Same shape as Story-1.5's
# scaffold-svc.sh-emitted template.

FROM golang:1.22-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/server /server
USER 65532
EXPOSE 8080
ENTRYPOINT ["/server"]
