#!/bin/sh
# Prints a go test -run pattern for the tests in files with the build tag $1,
# or nothing when there are none.
set -eu
tag=$1
names=$(git ls-files -co --exclude-standard -- '*_test.go' | while IFS= read -r f; do
	[ -f "$f" ] || continue
	head -n 1 "$f" | grep -q "^//go:build $tag\$" || continue
	sed -n 's/^func \(TestSCN_[A-Za-z0-9_]*\)(.*/\1/p' "$f"
done | sort -u | paste -sd '|' -)
[ -n "$names" ] && printf '^(%s)$' "$names"
exit 0
