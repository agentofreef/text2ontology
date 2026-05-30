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

# Run as a non-root user. The static binary needs no write access to the image,
# so a plain unprivileged account is sufficient.
RUN addgroup -S app && adduser -S -G app -u 10001 app

COPY --from=builder /service /service

# Ship the editable system prompts and point the handler at them, so the rich
# explore_system.md (not just the inline Go fallback) drives the LLM. Without
# this the container silently fell back to the compiled-in const.
COPY --from=builder /build/services/agent-server/prompts /prompts
ENV EXPLORE_SYSTEM_PROMPT_PATH=/prompts/explore_system.md

EXPOSE 8092

USER app
ENTRYPOINT ["/service"]
