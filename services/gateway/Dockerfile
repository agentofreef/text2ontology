# services/gateway/Dockerfile
#
# Single ingress nginx in front of the whole stack. Bakes the routing config
# so the only deployment artifact is "pull this image + run". Every other
# service binds to its container-network port only; the gateway is the sole
# host-port exposed by docker-compose.yml.
#
# Routing (see nginx.conf for the authoritative rules):
#   /                                            → frontend (static Next.js)
#   /api/ontology/lakehouse-agent-*|lh-test-*|... → agent-server (SSE-aware)
#   /api/ontology/*                              → backend-api
#   /api/auth|projects|prompt-config|llm-*       → backend-api
#   /api/connector/*, /connector/*               → collector-server
#   /api/mcp/*, /mcp                             → mcp-tools-server
#   /internal/*                                  → 404 (defense-in-depth)

FROM nginx:1.27-alpine

# Drop the default site; our nginx.conf is the entire http block.
RUN rm -f /etc/nginx/conf.d/default.conf
COPY services/gateway/nginx.conf /etc/nginx/nginx.conf

# Lightweight tools for the healthcheck and quick in-container diagnostics.
RUN apk add --no-cache wget curl

EXPOSE 80

HEALTHCHECK --interval=10s --timeout=3s --retries=5 --start-period=5s \
  CMD wget --spider -q http://127.0.0.1/healthz || exit 1
