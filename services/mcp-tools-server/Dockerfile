# services/mcp-tools-server/Dockerfile
#
# Phase 3 MCP: real build for mcp-tools-server. Same multi-stage
# workspace pattern as backend-api + recall-server + lakehouse-sql-server
# + agent-server. Build context is pkg/ + services/ + go.work only —
# no backend/ copy (per Phase 1 E3).

FROM golang:1.25-alpine AS builder
WORKDIR /build

COPY go.work go.work.sum* ./
COPY pkg/ ./pkg/
COPY services/ ./services/
COPY vendor/ ./vendor/
RUN go work edit -dropuse=./backend

ENV CGO_ENABLED=0
ENV GOFLAGS=-mod=vendor
ENV GOSUMDB=off

WORKDIR /build/services/mcp-tools-server
RUN go build -o /service ./cmd/server

FROM alpine:latest
RUN apk add --no-cache ca-certificates wget

COPY --from=builder /service /service

EXPOSE 8095

ENTRYPOINT ["/service"]
