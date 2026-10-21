FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/fsg-worker ./cmd/fsg-worker \
 && mkdir -p /out/var/lib/fsg/spill

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fsg-worker /usr/local/bin/fsg-worker
# Owned by nonroot so the named spill volume is writable on first mount.
COPY --from=build --chown=65532:65532 /out/var/lib/fsg /var/lib/fsg
USER nonroot
ENTRYPOINT ["/usr/local/bin/fsg-worker"]
