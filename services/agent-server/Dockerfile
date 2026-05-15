# services/agent-server/Dockerfile
# Phase 2 A4a: real build for agent-server. Same workspace-mode +
# pkg/*-only pattern as lakehouse-sql-server + recall-server.

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

WORKDIR /build/services/agent-server
RUN go build -o /service ./cmd/server

FROM alpine:latest
RUN apk add --no-cache ca-certificates wget

COPY --from=builder /service /service

EXPOSE 8092

ENTRYPOINT ["/service"]
