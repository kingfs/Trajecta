#!/usr/bin/env bash
#
# Rebuild the two Go binaries from the working tree and bake them into a
# container image on top of an existing base image.
#
# This is the local iteration loop: `task build:go` produces `server` (HTTP API
# and management surface) and `trajecta` (the server-less migration CLI, which
# owns `upgrade` and `layout`), and this script copies exactly those two files
# into a copy of a base image. No full image build -- the UI build included --
# is needed to test a backend change.
#
# Two details make this more than a `docker build`:
#
#   * the Postgres migrations are embedded (`//go:embed postgres-migrations/*.sql`
#     in ent/generate.go), so rebuilding the binaries is what puts a new
#     migration into the image; nothing else has to be copied
#   * the entrypoint is repointed at the server binary, because the base image
#     may predate the split into two binaries and still point it at the file
#     that is now the CLI
#
# The image being replaced is tagged `<image>-prev-<utc>` first, so a rollback
# stays a one-line `docker tag`. The base image defaults to the image the
# running service uses, which is what you want when iterating on a deployment.
#
# Usage:
#   scripts/postgres/rebuild-image.sh [--image TAG] [--base TAG] [--no-tag-prev]
#
# Environment: see scripts/postgres/common.sh, plus
#   TRAJECTA_OPS_IMAGE       image tag to build (default trajecta:local)
#   TRAJECTA_OPS_BASE_IMAGE  base image (default: the image the service runs)
#   GOFLAGS, GOCACHE, GOMODCACHE and DOCKER_CONFIG are honoured as usual; set
#   them when the defaults are not writable (sandboxes, CI).
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
IMAGE=${TRAJECTA_OPS_IMAGE:-trajecta:local}
BASE_IMAGE=${TRAJECTA_OPS_BASE_IMAGE:-}
TAG_PREV=1

while [ $# -gt 0 ]; do
  case "$1" in
    --image) IMAGE=${2:?--image needs a tag}; shift 2 ;;
    --base) BASE_IMAGE=${2:?--base needs a tag}; shift 2 ;;
    --no-tag-prev) TAG_PREV=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

[ -f "$REPO_ROOT/go.mod" ] || die "not a Trajecta checkout: $REPO_ROOT"
require_cmd go

if [ -z "$BASE_IMAGE" ]; then
  CID=$(dc ps -q "$TRAJECTA_OPS_SERVICE" 2>/dev/null || true)
  if [ -n "$CID" ]; then
    BASE_IMAGE=$(docker inspect "$CID" --format '{{.Image}}')
    note "base image taken from the running service: $BASE_IMAGE"
  else
    BASE_IMAGE=$IMAGE
    note "service not running; falling back to base image $BASE_IMAGE"
  fi
fi
docker image inspect "$BASE_IMAGE" >/dev/null 2>&1 || die "base image not found: $BASE_IMAGE (pass --base or build it first)"

section "1) build both binaries from $REPO_ROOT"
COMMIT=$(git -C "$REPO_ROOT" rev-parse --short HEAD)
BRANCH=$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION=$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || printf dev)
LDFLAGS="-s -w -X main.Version=$VERSION -X main.Commit=$COMMIT -X main.Date=$BUILD_DATE -X main.Branch=$BRANCH"
note "version=$VERSION commit=$COMMIT branch=$BRANCH date=$BUILD_DATE"
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o server ./cmd/server)
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o trajecta ./cmd/trajecta)
ls -la "$REPO_ROOT/server" "$REPO_ROOT/trajecta" | awk '{printf "  %10.1f MB %s\n", $5/1048576, $9}'

section "2) bake them into $IMAGE"
BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT
cp "$REPO_ROOT/server" "$REPO_ROOT/trajecta" "$BUILD_DIR/"
cat > "$BUILD_DIR/Dockerfile" <<EOF
FROM $BASE_IMAGE
COPY server /app/bin/server
COPY trajecta /app/bin/trajecta
ENTRYPOINT ["/app/bin/server"]
CMD ["serve", "-c", "/app/config/config.yaml"]
EOF
note "Dockerfile:"; sed 's/^/    /' "$BUILD_DIR/Dockerfile"
if [ "$TAG_PREV" = 1 ] && docker image inspect "$IMAGE" >/dev/null 2>&1; then
  PREV="$IMAGE-prev-$(date -u +%Y%m%d%H%M%S)"
  docker tag "$IMAGE" "$PREV"
  note "previous image kept as $PREV"
  note "rollback afterwards: docker tag $PREV $IMAGE"
fi
docker build -q -f "$BUILD_DIR/Dockerfile" -t "$IMAGE" "$BUILD_DIR" >/dev/null

section "3) verify the image"
docker image inspect "$IMAGE" --format '  id={{.Id}}'
docker image inspect "$IMAGE" --format '  entrypoint={{json .Config.Entrypoint}} cmd={{json .Config.Cmd}}'
docker run --rm --entrypoint "$TRAJECTA_OPS_CLI" "$IMAGE" version 2>&1 | head -2 | sed 's/^/  /'
note "run the deployment on it with: scripts/postgres/restart-and-verify.sh --expect-image $IMAGE"
