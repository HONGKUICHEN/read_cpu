#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_DIR="${HOME}/.config/systemd/user"
SERVICE_FILE="${SERVICE_DIR}/read_cpu.service"

echo ">>> install dependencies"
sudo apt-get update
sudo apt-get install -y golang-go

echo ">>> prepare directories"
mkdir -p "${SERVICE_DIR}"
mkdir -p "${PROJECT_DIR}/logs"

echo ">>> build binary"
cd "${PROJECT_DIR}"
go build -o "${PROJECT_DIR}/read_cpu" .

echo ">>> generate systemd user service"
"${PROJECT_DIR}/read_cpu" --service-file "${SERVICE_FILE}"

echo ">>> enable service"
systemctl --user daemon-reload
systemctl --user enable --now read_cpu.service

echo ">>> done"
echo "service file: ${SERVICE_FILE}"
echo "logs dir: ${PROJECT_DIR}/logs"
