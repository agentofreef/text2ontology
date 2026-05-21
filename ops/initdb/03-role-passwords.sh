#!/bin/sh
# ops/initdb/03-role-passwords.sh
#
# Product-ready bootstrap, step 3 (runs once, only on a FRESH data volume, after
# 01-schema.sql and 02-db-roles.sql). db-roles.sql creates the six per-service
# roles with LOGIN but deliberately NO password. This script sets each role's
# password to POSTGRES_PASSWORD so the services can authenticate as their own
# least-privilege role over the internal Docker network.
#
# Security model: privilege isolation comes from the per-role GRANTs in
# db-roles.sql (each service can only touch its own tables). The roles share the
# POSTGRES_PASSWORD value here purely to keep the single-host bundled-Postgres
# deployment zero-config. For stricter isolation, set distinct passwords per role
# (ALTER ROLE <role> PASSWORD '...') and update each *_DSN accordingly. The DB is
# never published to the host, so these credentials are not internet-reachable.
set -e

# Escape single quotes for safe interpolation into the SQL string literal.
escaped_pw=$(printf '%s' "$POSTGRES_PASSWORD" | sed "s/'/''/g")

for role in backend_api_user agent_server_user recall_server_user \
            lakehouse_sql_server_user mcp_tools_server_user collector_server_user; do
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    -c "ALTER ROLE ${role} PASSWORD '${escaped_pw}';"
done

echo "[initdb] per-service role passwords set from POSTGRES_PASSWORD"
