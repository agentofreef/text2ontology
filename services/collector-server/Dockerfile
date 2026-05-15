# services/collector-server/Dockerfile
#
# Phase 1: collector-server shell.
# Multi-stage: golang:1.25-alpine builder + python:3.11-slim runtime.
# Python is preserved for Phase 2 PBI shellout (pbix_to_csv.py + xlrd/openpyxl).

FROM golang:1.25-alpine AS builder
WORKDIR /build

COPY go.work go.work.sum* ./
COPY pkg/ ./pkg/
COPY services/ ./services/
COPY vendor/ ./vendor/
RUN go work edit -dropuse=./backend 2>/dev/null || true

ENV CGO_ENABLED=0
ENV GOFLAGS=-mod=vendor
ENV GOSUMDB=off

WORKDIR /build/services/collector-server
RUN go build -o /service ./cmd/server

# Runtime stage: python:3.11-slim (for Phase 2 PBI shellout)
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        wget \
    && rm -rf /var/lib/apt/lists/*

# Phase 2/PBIX: PBI shellout dependencies + scripts
# - xlrd / openpyxl: Excel header parse (PBIT + File source)
# - pandas / pbixray: PBIX 解析（pbix_to_csv.py + pbix_extract.py 必需）
# 用 tsinghua 镜像 + retries 应对 daocloud / 国内网络 hash mismatch
RUN pip install --no-cache-dir --retries 5 \
    --index-url https://pypi.tuna.tsinghua.edu.cn/simple/ \
    --trusted-host pypi.tuna.tsinghua.edu.cn \
    xlrd openpyxl pandas pbixray
COPY services/collector-server/scripts/ /app/scripts/
ENV PBIX_SCRIPTS_DIR=/app/scripts

COPY --from=builder /service /service

EXPOSE 8096

ENTRYPOINT ["/service"]
