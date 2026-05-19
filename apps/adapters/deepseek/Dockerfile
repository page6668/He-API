# syntax=docker/dockerfile:1
# Multi-stage build for the DeepSeek adapter (Story 4.1).
#
# Builder stage: vendored Go modules + connect-go + golang.org/x/net/http2.
# Runtime stage: distroless static so the image footprint matches the
# Story-3.1 api-gateway Dockerfile pattern.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/adapter-deepseek ./apps/adapters/deepseek/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/adapter-deepseek /adapter-deepseek
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/adapter-deepseek"]
