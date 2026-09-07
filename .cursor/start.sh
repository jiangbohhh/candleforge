#!/usr/bin/env bash
# CandleForge Cloud Agent start script.
# Per-boot runtime reconciliation: bring up PostgreSQL and ensure the
# candleforge role/database exist. Idempotent; returns once the DB is ready.
# The three application services run as long-lived `terminals` (see
# .cursor/environment.json), not here.
set -euo pipefail

# ── Start the PostgreSQL cluster (idempotent) ──
if ! pg_lsclusters -h 2>/dev/null | awk '$1=="16" && $2=="main"{print $4}' | grep -q online; then
  echo "==> Starting PostgreSQL 16/main"
  sudo pg_ctlcluster 16 main start || true
fi

# ── Wait for readiness ──
for _ in $(seq 1 30); do
  if sudo -u postgres psql -tAc 'SELECT 1' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! sudo -u postgres psql -tAc 'SELECT 1' >/dev/null 2>&1; then
  echo "PostgreSQL did not become ready in time" >&2
  exit 1
fi

# ── Ensure candleforge role + database (idempotent) ──
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='candleforge'" | grep -q 1; then
  sudo -u postgres psql -c "CREATE ROLE candleforge LOGIN PASSWORD 'candleforge';"
fi
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='candleforge'" | grep -q 1; then
  sudo -u postgres createdb -O candleforge candleforge
fi

echo "==> PostgreSQL ready (db=candleforge on localhost:5432)."
