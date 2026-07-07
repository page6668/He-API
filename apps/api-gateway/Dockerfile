# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/gateway ./apps/api-gateway/cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/gateway /gateway
# Story 1.5 wires the real HTTP listener on :8080.
ENTRYPOINT ["/gateway"]
