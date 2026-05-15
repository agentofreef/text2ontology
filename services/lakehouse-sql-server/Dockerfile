# services/lakehouse-sql-server/Dockerfile
#
# Phase 1 D4a: real lakehouse-sql-server build.
# Phase 1 E3: dropped the backend/ copy after llmclient + httputil +
# observability were promoted into pkg/. The service is now self-
# contained and builds from pkg/ + its own service tree only.

# ── build stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder
WORKDIR /build

# pkg/ + services/ + go.work is everything the service build needs.
# We drop ./backend (monolith) from the workspace since the backend
# module is no longer in the build context and the service code has
# zero remaining `lakehouse2ontology/*` imports.
COPY go.work go.work.sum* ./
COPY pkg/ ./pkg/
COPY services/ ./services/
COPY vendor/ ./vendor/
RUN go work edit -dropuse=./backend

ENV CGO_ENABLED=0
ENV GOFLAGS=-mod=vendor
ENV GOSUMDB=off

WORKDIR /build/services/lakehouse-sql-server
RUN go build -o /service ./cmd/server

# ── runtime stage ────────────────────────────────────────────────────────────
FROM alpine:latest
RUN apk add --no-cache ca-certificates wget

COPY --from=builder /service /service

EXPOSE 8094

ENTRYPOINT ["/service"]
