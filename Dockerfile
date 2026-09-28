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

RUN mkdir -p /app/bin /app/config /app/data/traces

WORKDIR /app

COPY --from=builder /out/server /app/bin/server
COPY --from=builder /out/trajecta /app/bin/trajecta
COPY config/config.yaml /app/config/config.yaml

VOLUME ["/app/config", "/app/data"]

EXPOSE 8080 8081

# A container runs a single entrypoint, so the CLI is invoked explicitly with the
# same image and the same mounts:
#   docker run --rm -v /host/data:/app/data -v /host/config:/app/config \
#     --entrypoint /app/bin/trajecta <image> upgrade env
ENTRYPOINT ["/app/bin/server"]
CMD ["serve", "-c", "/app/config/config.yaml"]
