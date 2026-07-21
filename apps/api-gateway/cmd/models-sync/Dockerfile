# syntax=docker/dockerfile:1.7
#
# models-sync — AD-002 模型目录发现任务(specs/model-catalogue-arch.md)。
# 定时拉上游可用模型清单,新 id 落 status='pending' 等人工补定价与能力;
# 上游已消失的 id 标 deprecated。永不删行、永不自动上架。
#
# 与 safety-log-retention 同构:多阶段构建 + distroless,Pod Security
# Restricted(UID 65532)。构建上下文是仓库根(go.work 下 api-gateway 依赖
# packages/*),与 build-images.yml 矩阵里的其余镜像一致。
#
#   docker build -f apps/api-gateway/cmd/models-sync/Dockerfile -t models-sync .

FROM golang:1.25-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOOS=linux
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/models-sync ./apps/api-gateway/cmd/models-sync

FROM gcr.io/distroless/static-debian12
# 上游走 HTTPS,distroless static 不带 CA 证书包外的根证书;
# static-debian12 已内置 /etc/ssl/certs/ca-certificates.crt,无需额外拷贝。
COPY --from=builder /out/models-sync /models-sync
USER 65532
ENTRYPOINT ["/models-sync"]
