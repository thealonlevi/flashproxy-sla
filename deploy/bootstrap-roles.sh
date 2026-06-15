#!/usr/bin/env bash
# Bootstraps the ClickHouse roles, settings profiles, and users for
# flashproxy-status WITHOUT leaking plaintext passwords into shell history.
#
# Passwords are read from the environment (never passed as argv), and history is
# disabled for this process. Statements are sent one per HTTP request over the
# loopback admin endpoint.
#
# IMPORTANT: CREATE USER ... IDENTIFIED WITH sha256_password BY '<plaintext>' puts
# the plaintext into ClickHouse's query_log. Configure a query_masking_rule in
# config.xml (see deploy/clickhouse-deploy-prompt.md) so it is redacted there, and
# set select_from_system_db_requires_grant=true so the published reader can't read
# query_log at all. Run this on the ClickHouse host, over loopback.
#
# Required env:
#   CH_ADMIN_URL   (e.g. http://127.0.0.1:8123)
#   CH_ADMIN_USER  CH_ADMIN_PASS   (an admin/access-management user)
#   SLA_PUBLIC_PASSWORD  SLA_WEBSITE_PASSWORD  SLA_WORKER_PASSWORD
set -euo pipefail
unset HISTFILE 2>/dev/null || true
set +o history 2>/dev/null || true

: "${CH_ADMIN_URL:?set CH_ADMIN_URL}"
: "${CH_ADMIN_USER:?set CH_ADMIN_USER}"
: "${CH_ADMIN_PASS:?set CH_ADMIN_PASS}"
: "${SLA_PUBLIC_PASSWORD:?set SLA_PUBLIC_PASSWORD}"
: "${SLA_WEBSITE_PASSWORD:?set SLA_WEBSITE_PASSWORD}"
: "${SLA_WORKER_PASSWORD:?set SLA_WORKER_PASSWORD}"

here="$(cd "$(dirname "$0")/.." && pwd)"
roles_sql="$here/schema/roles.sql"
[ -f "$roles_sql" ] || { echo "missing $roles_sql" >&2; exit 1; }

send() { # send one SQL statement via stdin
  curl -fsS --data-binary @- \
    -H "X-ClickHouse-User: $CH_ADMIN_USER" -H "X-ClickHouse-Key: $CH_ADMIN_PASS" \
    "$CH_ADMIN_URL/" >/dev/null
}

# Expand ${ENV} placeholders, strip comments/blank lines, then execute statement by
# statement (split on ';'). envsubst keeps the plaintext out of argv.
expanded="$(envsubst < "$roles_sql")"
printf '%s\n' "$expanded" \
  | sed -E 's/--.*$//' \
  | tr '\n' ' ' \
  | awk 'BEGIN{RS=";"} { gsub(/^[ \t]+|[ \t]+$/,""); if (length($0)>0) print $0 ";" }' \
  | while IFS= read -r stmt; do
      [ -n "${stmt%%;}" ] || continue
      printf '%s' "$stmt" | send
    done

echo "roles/profiles/users created. Reminder: set query_masking_rules + select_from_system_db_requires_grant in config.xml."
