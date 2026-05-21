# services/frontend/Dockerfile
#
# Phase 4C final: frontend becomes an independent deployable — builds
# Next.js static export in a node stage, then serves it via a thin
# nginx stage. Browser hits :18080 for UI; API calls go DIRECTLY to
# backend-api/agent-server/recall-server/mcp-tools-server (no gateway
# hop) — see services/backend-api/cmd/server/register_public_api.go +
# pkg/httputil/cors.go for the matching public /api/* surface + CORS
# allow-list.
#
# Build-args are baked into the static bundle at build time (Next.js
# NEXT_PUBLIC_* contract) so the browser knows which sidecar to call.

FROM node:24-alpine AS builder
WORKDIR /build

# Install deps
COPY frontend/package.json frontend/package-lock.json* frontend/pnpm-lock.yaml* ./
RUN npm config set registry https://registry.npmjs.org/ && \
    npm ci --no-audit --no-fund

# Build-time env pumped into Next.js; leave unset to fall back to the
# same-origin (legacy monolith gateway) behavior.
ARG NEXT_PUBLIC_BACKEND_API_URL=""
ARG NEXT_PUBLIC_AGENT_SERVER_URL=""
ARG NEXT_PUBLIC_RECALL_SERVER_URL=""
ENV NEXT_PUBLIC_BACKEND_API_URL=$NEXT_PUBLIC_BACKEND_API_URL
ENV NEXT_PUBLIC_AGENT_SERVER_URL=$NEXT_PUBLIC_AGENT_SERVER_URL
ENV NEXT_PUBLIC_RECALL_SERVER_URL=$NEXT_PUBLIC_RECALL_SERVER_URL

COPY frontend/ ./
RUN npm run build

# ───────────────────────────────────────────────────────────────────
FROM nginx:alpine

COPY --from=builder /build/out /usr/share/nginx/html
COPY services/frontend/nginx.conf /etc/nginx/conf.d/default.conf

# Run nginx as the non-root `nginx` user (uid 101, ships with the image).
# Point the pid file at /tmp and make the cache/runtime dirs writable so the
# master process doesn't need root. Listening on 8080 (see nginx.conf) keeps
# us off privileged ports.
RUN sed -i 's|pid        /var/run/nginx.pid;|pid        /tmp/nginx.pid;|' /etc/nginx/nginx.conf \
    && chown -R nginx:nginx /var/cache/nginx /usr/share/nginx/html \
    && chmod -R g+w /var/cache/nginx

EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget --spider -q http://127.0.0.1:8080/healthz || exit 1

USER nginx
