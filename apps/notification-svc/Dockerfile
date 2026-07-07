# syntax=docker/dockerfile:1.7
#
# notification-svc — He-API notification fan-out service (Story 2.2).
# Multi-stage build + distroless final image; Pod Security Restricted profile.

FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./apps/notification-svc/cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/server /server
USER 65532
EXPOSE 8080
ENTRYPOINT ["/server"]
