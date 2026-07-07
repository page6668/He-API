# syntax=docker/dockerfile:1.7
#
# auth-svc — He-API authentication service (Story 2.2).
# Multi-stage build + distroless final image; Pod Security Restricted profile
# (UID 65532) admits the pod into he-api-staging.

FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./apps/auth-svc/cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/server /server
USER 65532
EXPOSE 8080
ENTRYPOINT ["/server"]
