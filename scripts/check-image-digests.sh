#!/usr/bin/env bash
#
# Fail if any Dockerfile pins a base image to a malformed sha256 digest.
#
# A fabricated placeholder digest ("...4e4e4e4e4e") sat in database/Dockerfile
# and only surfaced as a Docker build failure, because the tag beside it looked
# right and nothing checked the digest itself. A sha256 digest is exactly 64
# lowercase hex characters; anything else can never resolve.
#
# This is a shape check, not a liveness check: it does not hit any registry, so
# it stays fast and works offline. A well-formed digest that has been garbage
# collected upstream will still fail at build time — see docs/CI.md.

set -euo pipefail

fail=0

while IFS= read -r file; do
    while IFS= read -r line; do
        digest=${line##*sha256:}
        # Strip anything trailing the digest (" AS builder", comments).
        digest=${digest%% *}
        length=${#digest}

        if [[ ${length} -ne 64 ]]; then
            echo "✗ ${file}: digest is ${length} characters, expected 64"
            echo "    ${line}"
            fail=1
        elif [[ ! ${digest} =~ ^[a-f0-9]+$ ]]; then
            echo "✗ ${file}: digest contains non-hex characters"
            echo "    ${line}"
            fail=1
        fi
    done < <(grep -oE "FROM +[^ ]+@sha256:[^ ]+" "${file}" || true)
done < <(find . -name "Dockerfile*" -not -path "*/node_modules/*" -not -path "*/target/*")

if [[ ${fail} -ne 0 ]]; then
    echo
    echo "Fix: resolve the real digest before committing, e.g."
    echo "  docker buildx imagetools inspect <image>:<tag> --format '{{.Manifest.Digest}}'"
    exit 1
fi

count=$(find . -name "Dockerfile*" -not -path "*/node_modules/*" -not -path "*/target/*" \
    -exec grep -oE "FROM +[^ ]+@sha256:[^ ]+" {} + | wc -l)
echo "OK: ${count} pinned base image digests, all well-formed."
