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
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG BRANCH=unknown

COPY go.mod go.sum ./
RUN go env -w GOPROXY="${GOPROXY}" GOSUMDB="${GOSUMDB}" && \
	go mod download

COPY . .

RUN go build -trimpath \
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

COPY --from=builder /out/server /app/bin/server
COPY --from=builder /out/trajecta /app/bin/trajecta
COPY config/config.yaml /app/config/config.yaml

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
