#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${PROJECT_DIR}"
if [ ! -x "${PROJECT_DIR}/read_cpu" ]; then
  go build -o "${PROJECT_DIR}/read_cpu" .
fi
exec "${PROJECT_DIR}/read_cpu" "$@"
