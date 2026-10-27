#!/bin/sh
set -e

if [ "${PSG_AUTO_MIGRATE:-true}" = "true" ]; then
  # Wait up to ~60 s for MariaDB; after that exit and let the restart policy retry.
  attempt=1
  until alembic -c /app/alembic.ini upgrade head 2>/tmp/migrate.err; do
    if [ "$attempt" -ge 20 ]; then
      cat /tmp/migrate.err >&2
      echo '{"level":"error","msg":"migrations failed; mariadb unreachable?"}' >&2
      exit 1
    fi
    echo "{\"level\":\"warning\",\"msg\":\"waiting for mariadb (attempt $attempt)\"}"
    attempt=$((attempt + 1))
    sleep 3
  done
fi

exec python -m psg_control "$@"
