#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_DIR="${HOME}/.config/systemd/user"
SERVICE_FILE="${SERVICE_DIR}/read_cpu.service"

echo ">>> install dependencies"
sudo apt-get update
sudo apt-get install -y python3

echo ">>> prepare directories"
mkdir -p "${SERVICE_DIR}"
mkdir -p "${PROJECT_DIR}/logs"

echo ">>> generate systemd user service"
python3 "${PROJECT_DIR}/monitor.py" --service-file "${SERVICE_FILE}"

echo ">>> enable service"
systemctl --user daemon-reload
systemctl --user enable --now read_cpu.service

echo ">>> done"
echo "service file: ${SERVICE_FILE}"
echo "logs dir: ${PROJECT_DIR}/logs"
