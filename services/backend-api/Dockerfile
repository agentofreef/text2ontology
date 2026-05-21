# services/backend-api/Dockerfile
#
# Phase 3 B1: real build for backend-api. Same multi-stage workspace
# pattern as recall-server + lakehouse-sql-server + agent-server after
# Phase 1 E3 dropped the backend/ copy — build context is pkg/ +
# services/ + go.work only.
#
# This Dockerfile is NOT yet referenced by docker-compose.yml. Phase 3
# B4 will swap the compose stanza from Dockerfile.service-stub →
# services/backend-api/Dockerfile once the shadow-copy handlers in B2
# + route wiring in B3 are verified locally.

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

WORKDIR /build/services/backend-api
RUN go build -o /service ./cmd/server

FROM alpine:latest
RUN apk add --no-cache ca-certificates wget

# Run as a non-root user. The static binary needs no write access to the image,
# so a plain unprivileged account is sufficient.
RUN addgroup -S app && adduser -S -G app -u 10001 app

COPY --from=builder /service /service

EXPOSE 8090

USER app
ENTRYPOINT ["/service"]
