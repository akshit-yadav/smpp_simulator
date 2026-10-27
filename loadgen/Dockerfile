# psg-tester: PSG's SMPP client simulator agent.
# Build context: the repository root (the SMPP codec lives in the worker module);
# loadgen/Dockerfile.dockerignore keeps the context to worker/ and loadgen/.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY worker/go.mod worker/go.sum worker/
COPY loadgen/go.mod loadgen/go.sum loadgen/
RUN cd loadgen && GOWORK=off go mod download
COPY worker/ worker/
COPY loadgen/ loadgen/
ARG VERSION=dev
RUN cd loadgen && CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/psg-tester ./cmd/psg-tester

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/psg-tester /usr/local/bin/psg-tester
USER nonroot
ENTRYPOINT ["/usr/local/bin/psg-tester"]
