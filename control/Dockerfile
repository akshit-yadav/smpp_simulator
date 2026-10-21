# Build context: repository root (the image also serves the built panel).

FROM node:22-alpine AS panel
WORKDIR /panel
COPY panel/package.json panel/package-lock.json ./
RUN npm ci
COPY panel/ ./
RUN npm run build

FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PSG_PANEL_DIR=/app/panel_dist
WORKDIR /app
COPY control/pyproject.toml ./
COPY control/psg_control ./psg_control
RUN pip install .
COPY control/alembic.ini ./
COPY control/migrations ./migrations
COPY control/clickhouse ./clickhouse
COPY control/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY --from=panel /panel/dist ./panel_dist
RUN useradd --system --uid 10001 --no-create-home psg
USER psg
ENTRYPOINT ["docker-entrypoint.sh"]
