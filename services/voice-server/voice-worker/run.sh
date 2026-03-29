#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
export PYTHONPATH="${PYTHONPATH:-}:$(pwd)"
exec python -m uvicorn app.main:app --host "${VOICE_SERVER_HOST:-0.0.0.0}" --port "${VOICE_SERVER_PORT:-8000}"
