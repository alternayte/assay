#!/bin/sh
# Browser end-to-end tests (layer E). Builds the image, starts a separate
# Compose project on a fresh database and runs Playwright against it.
#
#   sh scripts/e2e.sh <reports dir>
set -eu

reports=$1
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
export COMPOSE_PROJECT_NAME=assay-e2e
port=18080

compose() { docker compose --project-directory "$work" -f "$work/compose.yaml" "$@"; }
cleanup() {
	compose down -v >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

docker build -q -t assay:e2e "$root" >/dev/null
cp "$root/deploy/compose/compose.yaml" "$work/compose.yaml"
cat >"$work/.env" <<ENV
ASSAY_IMAGE=assay:e2e
ASSAY_PORT=$port
POSTGRES_PASSWORD=e2e-password
ASSAY_SECRET_KEY=$(printf 'e2e-secret-key-of-32-bytes-long!' | base64)
ASSAY_SETUP_TOKEN=e2e-setup-token
ASSAY_BASE_URL=http://localhost:$port
ASSAY_LOG_LEVEL=info
ENV

compose down -v >/dev/null 2>&1 || true
compose up -d --wait db >/dev/null
compose up -d assay >/dev/null
i=0
until curl -fsS "http://localhost:$port/readyz" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -ge 60 ]; then
		compose logs assay >&2
		exit 1
	fi
	sleep 1
done

cd "$root/web"
status=0
ASSAY_REPORTS="$reports" E2E_BASE_URL="http://localhost:$port" E2E_SETUP_TOKEN=e2e-setup-token bunx playwright test || status=$?
[ "$status" -eq 0 ] || compose logs --tail 100 assay >&2
exit "$status"
