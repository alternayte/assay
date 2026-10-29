#!/bin/sh
# Fails on placeholder words in source files and on private words anywhere in
# the tracked tree and the commit messages.
set -eu
cd "$(dirname "$0")/.."

status=0

sources=$(git ls-files -co --exclude-standard -- '*.go' '*.ts' '*.tsx' '*.sh' '*.yaml' '*.sql' |
	while IFS= read -r f; do
		[ -f "$f" ] || continue
		head -n 3 "$f" | grep -q 'Code generated .* DO NOT EDIT' && continue
		printf '%s\n' "$f"
	done)

if [ -n "$sources" ]; then
	hits=$(printf '%s\n' "$sources" | tr '\n' '\0' | xargs -0 grep -nF -f scripts/forbid-words.txt -- || true)
	if [ -n "$hits" ]; then
		printf 'forbid: placeholder words found:\n%s\n' "$hits"
		status=1
	fi
fi

private=.build/private-words.txt
if [ -s "$private" ]; then
	tracked=$(git ls-files -co --exclude-standard | while IFS= read -r f; do [ -f "$f" ] && printf '%s\n' "$f"; done)
	hits=$(printf '%s\n' "$tracked" | tr '\n' '\0' | xargs -0 grep -nliF -f "$private" -- || true)
	if [ -n "$hits" ]; then
		printf 'forbid: private words found in files:\n%s\n' "$hits"
		status=1
	fi
	if git rev-parse --verify -q HEAD >/dev/null; then
		commits=$(git log --format='%H %B' | grep -iF -f "$private" | cut -c1-12 || true)
		if [ -n "$commits" ]; then
			printf 'forbid: private words found in commit messages:\n%s\n' "$commits"
			status=1
		fi
	fi
fi

exit "$status"
