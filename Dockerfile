FROM golang:1.25-alpine AS builder

WORKDIR /src

ENV CGO_ENABLED=0

ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG http_proxy
ARG https_proxy
ARG no_proxy

COPY go.mod go.sum ./
RUN go env -w GOPROXY="${GOPROXY}" GOSUMDB="${GOSUMDB}" && \
	go mod download

# Only the four trees the two binaries actually import are copied, instead of
# `COPY . .`. `go list -deps ./cmd/server ./cmd/trajecta` resolves every
# in-module dependency inside cmd/, internal/, pkg/ and ent/ (ent also holds the
# //go:embed'ed migrations, internal/ the //go:embed'ed monitor UI assets), so
# nothing else is a build input. With `COPY . .` an edit to any file anywhere -
# a doc, the README, a Monitor UI source file - changed this layer and threw
# away the compiled dependency graph underneath it, forcing a full recompile.
# One COPY per tree, least volatile first: an edit in internal/ (most of the
# code) then re-runs a single COPY, where the reverse order would re-copy the
# generated tree on every edit. Add a COPY line if a new top-level package
# enters that import graph.
COPY ent ./ent
COPY pkg ./pkg
COPY cmd ./cmd
COPY internal ./internal

# The version metadata is declared here rather than at the top of the stage
# because Docker includes the value of every ARG in scope in a RUN's cache key,
# even when the command never mentions it, and BUILD_DATE is a fresh timestamp
# on every `task docker:build`. With these four declared above `go mod download`
# the whole module graph was re-fetched on every build - measured at 23-27s,
# including a build with no repository change at all.
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG BRANCH=unknown

# The Go build cache is a BuildKit cache rather than layer content, so it
# survives the source COPY above and is reused across builds instead of being
# recompiled from scratch every time. It stays build-local: nothing here ends up
# in the image. `sharing=locked` keeps two concurrent builds from corrupting one
# cache.
#
# This needs BuildKit, which is the only builder Docker 29 has. The directive
# `# syntax=docker/dockerfile:1` is deliberately omitted: the bundled frontend
# already understands cache mounts, and adding it would make every build pull a
# frontend image from Docker Hub first.
RUN --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
	go build -trimpath \
	-ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.Date=${BUILD_DATE} -X main.Branch=${BRANCH}" \
	-o /out/server ./cmd/server && \
	go build -trimpath \
	-ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.Date=${BUILD_DATE} -X main.Branch=${BRANCH}" \
	-o /out/trajecta ./cmd/trajecta

FROM alpine:3.22 AS runtime

ENV APP_HOME=/app \
	TRAJECTA_CONFIG=/app/config/config.yaml

ENV TZ=UTC \
	TRAJECTA_OUTPUT_DIR=/app/data/traces \
	TRAJECTA_TRACE_OUTPUT_DIR=/app/data/traces

# tzdata is not part of the alpine base image, and the Monitor resolves its
# display timezone with time.LoadLocation. Both binaries also embed the IANA
# database, so this keeps the image consistent for anything else that reads TZ.
RUN apk add --no-cache tzdata && \
	mkdir -p /app/bin /app/config /app/data/traces

WORKDIR /app

# config.yaml is copied before the binaries and the two binaries are copied in
# one instruction, because the binaries are relinked on every build - BUILD_DATE
# is part of their link inputs, so the builder layer they come from is never
# reused as-is. Anything ordered after them therefore re-runs on every build: in
# this stage that was one ~1.3s step per file, most of it per-step overhead
# rather than bytes moved.
COPY config/config.yaml /app/config/config.yaml
COPY --from=builder /out/ /app/bin/

# /app/data is a volume: the cassettes are state the container must not carry
# inside its own layer.
#
# /app/config deliberately is not. Declaring it a volume made Docker create an
# anonymous volume on first run and then *reuse* it on every
# `docker compose up --force-recreate`, so the copy of config.yaml baked into the
# image was shadowed by that first container's file forever. On the reference
# deployment that silently discarded two settings for months: the read-only pool
# (`read_max_open_conns`, whose code default is 0, so every Monitor read shared
# the proxy's write pool) and `use_session_summary_read`. Copying a new image
# never propagated a config change, and nothing reported it. Operators who want
# their own file mount it explicitly, which already overrides the image's copy:
#   - ./config/config.yaml:/app/config/config.yaml:ro
VOLUME ["/app/data"]

EXPOSE 8080 8081

# A container runs a single entrypoint, so the CLI is invoked explicitly with the
# same image and the same mounts:
#   docker run --rm -v /host/data:/app/data -v /host/config:/app/config \
#     --entrypoint /app/bin/trajecta <image> upgrade env
ENTRYPOINT ["/app/bin/server"]
CMD ["serve", "-c", "/app/config/config.yaml"]
