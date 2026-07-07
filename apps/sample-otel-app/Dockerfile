# syntax=docker/dockerfile:1.7
#
# sample-otel-app — Epic 1 reference implementation (Story 1.4).
# Multi-stage build + distroless final image so the Pod Security Restricted
# profile (UID 65532, readOnlyRootFilesystem, dropped capabilities) can admit
# the pod into the `he-api-staging` namespace.

FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/sample-otel-app ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/sample-otel-app /sample-otel-app
USER 65532
ENTRYPOINT ["/sample-otel-app"]
