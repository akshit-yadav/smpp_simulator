#!/bin/sh
# Create .env from .env.example, replacing every __generate__ with a random secret.
set -eu
cd "$(dirname "$0")/.."
if [ -f .env ]; then
  echo ".env already exists; leaving it alone" >&2
  exit 0
fi
cp .env.example .env
while grep -q '__generate__' .env; do
  line=$(grep -n '__generate__' .env | head -n1 | cut -d: -f1)
  key=$(sed -n "${line}p" .env | cut -d= -f1)
  case "$key" in
    FSG_SECRET_KEY) value=$(openssl rand -base64 32) ;;
    FSG_INTERNAL_TOKEN) value=$(openssl rand -hex 32) ;;
    *) value=$(openssl rand -hex 16) ;;
  esac
  sed -i "${line}s|__generate__|${value}|" .env
done
chmod 600 .env
echo "Wrote .env with generated secrets (admin password: FSG_ADMIN_PASSWORD)"
