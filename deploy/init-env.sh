#!/bin/sh
# Create .env from .env.example, replacing every __generate__ with a random secret.
set -eu
cd "$(dirname "$0")/.."
# Gateway file for the worker (until the control plane serves gateways).
mkdir -p config
if [ ! -f config/gateways.json ]; then
  cp deploy/gateways.example.json config/gateways.json
  while grep -q '__generate__' config/gateways.json; do
    sed -i "0,/__generate__/s//$(openssl rand -hex 4)/" config/gateways.json
  done
  chmod 644 config/gateways.json # the worker container runs as a non-root user
  echo "Wrote config/gateways.json (sample gateways, used only when PSG_GATEWAYS_FILE is set)"
fi

if [ -f .env ]; then
  echo ".env already exists; leaving it alone" >&2
  exit 0
fi
cp .env.example .env
while grep -q '__generate__' .env; do
  line=$(grep -n '__generate__' .env | head -n1 | cut -d: -f1)
  key=$(sed -n "${line}p" .env | cut -d= -f1)
  case "$key" in
    PSG_SECRET_KEY) value=$(openssl rand -base64 32) ;;
    PSG_INTERNAL_TOKEN | PSG_TESTER_TOKEN) value=$(openssl rand -hex 32) ;;
    *) value=$(openssl rand -hex 16) ;;
  esac
  sed -i "${line}s|__generate__|${value}|" .env
done
chmod 600 .env
echo "Wrote .env with generated secrets (admin password: PSG_ADMIN_PASSWORD)"
