#!/bin/sh
# Install and start Assay with Docker Compose.
#
#   curl -fsSL https://github.com/alternayte/assay/releases/latest/download/install.sh | sh
#
# Flags:
#   --dir DIR        Install directory. Default: $HOME/.assay
#   --port PORT      Port on 127.0.0.1. Default: 8080
#   --version VER    Release tag, such as v0.1.0. Default: latest
#   --down           Stop Assay. The data stays.
#
# Variables:
#   ASSAY_IMAGE        Use this image. A local image is not pulled.
#   ASSAY_INSTALL_SRC  Read compose.yaml and .env.example from this directory
#                      or URL instead of the release.
set -eu

dir="${HOME}/.assay"
port=8080
version=latest
down=0

say() { printf '%s\n' "$*"; }
fail() {
	printf 'assay install: %s\n' "$*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--dir)
		[ $# -ge 2 ] || fail "--dir needs a value."
		dir=$2
		shift 2
		;;
	--port)
		[ $# -ge 2 ] || fail "--port needs a value."
		port=$2
		shift 2
		;;
	--version)
		[ $# -ge 2 ] || fail "--version needs a value."
		version=$2
		shift 2
		;;
	--down)
		down=1
		shift
		;;
	-h | --help)
		sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*) fail "unknown flag $1. Use --help." ;;
	esac
done

case "$port" in
'' | *[!0-9]*) fail "--port must be a number." ;;
esac

docker_hint="Install Docker Desktop from https://www.docker.com/products/docker-desktop/ and start it. Then run this script again."
command -v docker >/dev/null 2>&1 || fail "Docker is not installed. $docker_hint"
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is not available. $docker_hint"
command -v curl >/dev/null 2>&1 || fail "curl is not installed. Install curl and run this script again."
docker info >/dev/null 2>&1 || fail "The Docker daemon does not respond. Start Docker Desktop and wait until it runs. Then run this script again."

mkdir -p "$dir"
cd "$dir"

compose() { docker compose --project-directory "$dir" -f "$dir/compose.yaml" "$@"; }

if [ "$down" -eq 1 ]; then
	[ -f compose.yaml ] || fail "No install in $dir."
	compose down
	say "Assay is stopped. The data stays in the Docker volume. Run the script again to start it."
	exit 0
fi

if [ "$version" = latest ]; then
	release="https://github.com/alternayte/assay/releases/latest/download"
else
	release="https://github.com/alternayte/assay/releases/download/$version"
fi
src="${ASSAY_INSTALL_SRC:-$release}"

fetch() {
	case "$src" in
	http://* | https://*)
		command -v curl >/dev/null 2>&1 || fail "curl is not installed."
		curl -fsSL "$src/$1" -o "$2.tmp" || fail "cannot download $src/$1."
		mv "$2.tmp" "$2"
		;;
	*)
		[ -f "$src/$1" ] || fail "$src/$1 does not exist."
		cp "$src/$1" "$2"
		;;
	esac
}

fetch compose.yaml compose.yaml
fetch .env.example .env.example

random_base64() {
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -base64 "$1"
	else
		head -c "$1" /dev/urandom | base64 | tr -d '\n'
	fi
}

random_token() {
	random_base64 "$1" | tr -d '=+/\n' | cut -c1-"$2"
}

# set_var NAME VALUE: replace the line NAME= in .env.
set_var() {
	awk -v k="$1" -v v="$2" 'BEGIN { FS = OFS = "=" } $1 == k { print k "=" v; next } { print }' .env >.env.tmp
	mv .env.tmp .env
}

first=0
if [ ! -f .env ]; then
	first=1
	umask 077
	cp .env.example .env
	set_var ASSAY_SECRET_KEY "$(random_base64 32)"
	set_var POSTGRES_PASSWORD "$(random_token 32 32)"
	set_var ASSAY_SETUP_TOKEN "$(random_token 32 24)"
	set_var ASSAY_PORT "$port"
	set_var ASSAY_VERSION "$version"
	set_var ASSAY_BASE_URL "http://localhost:$port"
	for v in HTTP_PROXY HTTPS_PROXY; do
		eval "value=\${$v:-}"
		[ -n "$value" ] && set_var "$v" "$value"
	done
	if [ -n "${NO_PROXY:-}" ]; then
		set_var NO_PROXY "$NO_PROXY,db,localhost,127.0.0.1"
	fi
	if [ -n "${ASSAY_IMAGE:-}" ]; then
		printf 'ASSAY_IMAGE=%s\n' "$ASSAY_IMAGE" >>.env
	fi
	say "Created $dir/.env with a new secret key. Keep a backup of this file."
fi

# The port and the image come from .env, so that a second run keeps them.
port=$(sed -n 's/^ASSAY_PORT=//p' .env | tail -n 1)
image=$(sed -n 's/^ASSAY_IMAGE=//p' .env | tail -n 1)
image="${ASSAY_IMAGE:-$image}"
if [ -n "$image" ]; then
	export ASSAY_IMAGE="$image"
fi

for img in $(compose config --images); do
	if [ "$img" = "$image" ] && docker image inspect "$img" >/dev/null 2>&1; then
		say "Using the local image $img."
		continue
	fi
	docker pull -q "$img" >/dev/null || fail "cannot pull $img. Check the network and the proxy settings."
done
compose up -d db
compose up -d --force-recreate assay

url="http://localhost:$port"
say "Waiting for Assay at $url ..."
i=0
until curl -fsS "$url/readyz" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -ge 120 ]; then
		compose logs --tail 50 assay >&2 || true
		fail "Assay did not become ready in 120 seconds. The log is above."
	fi
	sleep 1
done

token=$(sed -n 's/^ASSAY_SETUP_TOKEN=//p' .env | tail -n 1)
say ""
say "Assay runs at $url"
if [ "$first" -eq 1 ]; then
	say "Setup token: $token"
	say "Next step: open $url/setup and create the first admin with the setup token."
else
	say "Assay is updated and restarted. Your data and .env did not change."
	say "If setup is not done yet, open $url/setup. Setup token: $token"
fi
say "Stop Assay: sh install.sh --down --dir $dir"
