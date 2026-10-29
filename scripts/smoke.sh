#!/bin/sh
# Smoke tests of the install script and the image (layer S). Each scenario
# prints one JSON line to the report file.
#
#   sh scripts/smoke.sh <report.jsonl>
set -eu

report=$1
root=$(cd "$(dirname "$0")/.." && pwd)
: >"$report"
work=$(mktemp -d)
image=assay:smoke
status=0
# A separate project, so that the smoke test never touches a real install.
export COMPOSE_PROJECT_NAME=assay-smoke

now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

result() {
	# result SCN PASS MS [DETAIL]
	detail=$(printf '%s' "${4:-}" | tr '"\n' "' ")
	printf '{"scn":"%s","pass":%s,"ms":%s,"detail":"%s"}\n' "$1" "$2" "$3" "$detail" | tee -a "$report"
	[ "$2" = true ] || status=1
}

cleanup() {
	if [ -f "$work/install/compose.yaml" ]; then
		docker compose --project-directory "$work/install" -f "$work/install/compose.yaml" down -v >/dev/null 2>&1 || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT

say() { printf 'smoke: %s\n' "$*" >&2; }

say "building $image"
docker build -q -t "$image" "$root" >/dev/null

# A free port on 127.0.0.1.
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')

# SCN-INS-003: no docker, and a docker whose daemon does not answer.
tools="$work/tools"
mkdir -p "$tools"
for t in sh awk sed cut tr head cp mv mkdir cat curl openssl base64 sleep printf env; do
	p=$(command -v "$t" 2>/dev/null || true)
	[ -n "$p" ] && ln -s "$p" "$tools/$t"
done
start=$(now_ms)
out=$(PATH="$tools" HOME="$work/home" sh "$root/scripts/install.sh" --dir "$work/none" 2>&1) && code=0 || code=$?
case "$code:$out" in
0:*) result SCN-INS-003 false $(($(now_ms) - start)) "exit 0 without docker" ;;
*Docker\ Desktop*)
	fake="$work/fake"
	mkdir -p "$fake"
	cat >"$fake/docker" <<'EOF'
#!/bin/sh
[ "$1" = compose ] && exit 0
exit 1
EOF
	chmod +x "$fake/docker"
	out2=$(PATH="$fake:$tools" HOME="$work/home" sh "$root/scripts/install.sh" --dir "$work/none" 2>&1) && code2=0 || code2=$?
	case "$code2:$out2" in
	0:*) result SCN-INS-003 false $(($(now_ms) - start)) "exit 0 with a failing docker info" ;;
	*Docker\ Desktop*) result SCN-INS-003 true $(($(now_ms) - start)) ;;
	*) result SCN-INS-003 false $(($(now_ms) - start)) "$out2" ;;
	esac
	;;
*) result SCN-INS-003 false $(($(now_ms) - start)) "$out" ;;
esac

# SCN-INS-001: one run installs, starts and prints the URL and the token.
dir="$work/install"
start=$(now_ms)
out=$(ASSAY_IMAGE=$image ASSAY_INSTALL_SRC="$root/deploy/compose" sh "$root/scripts/install.sh" --dir "$dir" --port "$port" 2>&1) && code=0 || code=$?
ms=$(($(now_ms) - start))
token=$(sed -n 's/^ASSAY_SETUP_TOKEN=//p' "$dir/.env" 2>/dev/null || true)
bound=$(docker compose --project-directory "$dir" -f "$dir/compose.yaml" port assay 8080 2>/dev/null || true)
if [ "$code" -ne 0 ]; then
	result SCN-INS-001 false "$ms" "$out"
elif [ "$ms" -gt 120000 ]; then
	result SCN-INS-001 false "$ms" "readyz after more than 120 s"
elif [ "$bound" != "127.0.0.1:$port" ]; then
	result SCN-INS-001 false "$ms" "bound to $bound"
elif ! printf '%s' "$out" | grep -q "http://localhost:$port" || ! printf '%s' "$out" | grep -q "$token"; then
	result SCN-INS-001 false "$ms" "the output lacks the URL or the token"
else
	result SCN-INS-001 true "$ms"
fi

# Create the admin, so that the second run can check the sign-in.
base="http://127.0.0.1:$port"
curl -fsS -o /dev/null -H 'Content-Type: application/json' -H "Origin: http://localhost:$port" \
	-d "{\"token\":\"$token\",\"email\":\"owner@acme.test\",\"name\":\"Owner\",\"password\":\"owner-password-1\"}" \
	"$base/api/v1/setup" || say "setup failed"

signin() {
	curl -fsS -o /dev/null -H 'Content-Type: application/json' -H "Origin: http://localhost:$port" \
		-d '{"email":"owner@acme.test","password":"owner-password-1"}' "$base/api/auth/sign-in/email"
}

# SCN-INS-002: a second run keeps .env and data and restarts the container.
start=$(now_ms)
sum1=$(cksum <"$dir/.env")
started1=$(docker inspect -f '{{.State.StartedAt}}' "$(docker compose --project-directory "$dir" -f "$dir/compose.yaml" ps -q assay)")
out=$(ASSAY_IMAGE=$image ASSAY_INSTALL_SRC="$root/deploy/compose" sh "$root/scripts/install.sh" --dir "$dir" --port "$port" 2>&1) && code=0 || code=$?
sum2=$(cksum <"$dir/.env")
started2=$(docker inspect -f '{{.State.StartedAt}}' "$(docker compose --project-directory "$dir" -f "$dir/compose.yaml" ps -q assay)")
detail=""
[ "$code" -eq 0 ] || detail="second run failed: $out"
[ "$sum1" = "$sum2" ] || detail="$detail .env changed"
[ "$started1" != "$started2" ] || detail="$detail the container did not restart"
signin || detail="$detail the admin cannot sign in"
sh "$root/scripts/install.sh" --dir "$dir" --down >/dev/null 2>&1 || detail="$detail --down failed"
running=$(docker compose --project-directory "$dir" -f "$dir/compose.yaml" ps -q 2>/dev/null | wc -l | tr -d ' ')
[ "$running" = 0 ] || detail="$detail containers still run after --down"
docker volume inspect assay-smoke_db >/dev/null 2>&1 || detail="$detail the data volume is gone"
if [ -z "$detail" ]; then
	result SCN-INS-002 true $(($(now_ms) - start))
else
	result SCN-INS-002 false $(($(now_ms) - start)) "$detail"
fi

# SCN-INS-005: the image has amd64 and arm64, a non-root user, and 80 MB or
# less for each architecture.
start=$(now_ms)
docker buildx inspect assay-smoke >/dev/null 2>&1 || docker buildx create --name assay-smoke --driver docker-container >/dev/null
detail=""
for arch in amd64 arm64; do
	docker buildx build -q --builder assay-smoke --platform "linux/$arch" --load -t "assay:smoke-$arch" "$root" >/dev/null ||
		detail="$detail build of $arch failed"
	got=$(docker image inspect -f '{{.Architecture}} {{.Config.User}} {{.Size}}' "assay:smoke-$arch" 2>/dev/null || true)
	set -- $got
	[ "${1:-}" = "$arch" ] || detail="$detail $arch image has architecture ${1:-none}"
	case "${2:-}" in "" | root | 0 | 0:*) detail="$detail $arch image runs as root" ;; esac
	[ "${3:-0}" -le 83886080 ] || detail="$detail $arch image is ${3} bytes"
	printf '{"scn":"NFR-003","arch":"%s","bytes":%s}\n' "$arch" "${3:-0}" >>"$report"
done
if [ -z "$detail" ]; then
	result SCN-INS-005 true $(($(now_ms) - start))
else
	result SCN-INS-005 false $(($(now_ms) - start)) "$detail"
fi

exit "$status"
