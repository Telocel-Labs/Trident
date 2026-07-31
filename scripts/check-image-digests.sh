#!/usr/bin/env bash
#
# Fail if any Dockerfile pins a base image to a digest that is malformed or
# does not exist in the registry.
#
# Two fabricated digests reached this branch: one 60 characters long, and one
# the right length but pointing at nothing. Both looked plausible next to a
# correct-looking tag, and both only surfaced after a full image build. A
# shape check alone would have caught the first and missed the second, so this
# resolves every digest against the registry.
#
# Uses the anonymous Docker Hub pull API — no credentials needed. A registry
# that cannot be reached is reported as a skip, not a failure, so a network
# blip does not fail the build on something unrelated to the change.

set -euo pipefail

fail=0
checked=0
skipped=0

registry_has() {
    # $1 = repository (e.g. library/rust), $2 = sha256:... digest
    local repo=$1 digest=$2 token status
    token=$(curl -fsS --max-time 20 \
        "https://auth.docker.io/token?service=registry.docker.io&scope=repository:${repo}:pull" \
        | sed -n 's/.*"token":"\([^"]*\)".*/\1/p') || return 2
    [[ -z ${token} ]] && return 2

    status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 -I \
        -H "Authorization: Bearer ${token}" \
        -H "Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.docker.distribution.manifest.v2+json" \
        "https://registry-1.docker.io/v2/${repo}/manifests/${digest}") || return 2

    case ${status} in
        200) return 0 ;;
        404) return 1 ;;
        *)   return 2 ;;
    esac
}

while IFS= read -r file; do
    while IFS= read -r ref; do
        # ref looks like: rust:1.88-slim@sha256:abc...
        image=${ref%%@*}
        digest=${ref##*@}
        name=${image%%:*}

        # Only Docker Hub official/namespaced images are resolvable here.
        if [[ ${name} == *.*/* ]]; then
            echo "· ${file}: ${name} is not on Docker Hub, skipping resolution"
            skipped=$((skipped + 1))
            continue
        fi
        [[ ${name} != */* ]] && name="library/${name}"

        hex=${digest#sha256:}
        if [[ ${#hex} -ne 64 || ! ${hex} =~ ^[a-f0-9]+$ ]]; then
            echo "✗ ${file}: malformed digest (${#hex} chars, expected 64 hex)"
            echo "    ${ref}"
            fail=1
            continue
        fi

        if registry_has "${name}" "${digest}"; then
            checked=$((checked + 1))
        else
            case $? in
                1)
                    echo "✗ ${file}: digest does not exist in the registry"
                    echo "    ${ref}"
                    fail=1
                    ;;
                *)
                    echo "· ${file}: could not reach registry for ${name}, skipping"
                    skipped=$((skipped + 1))
                    ;;
            esac
        fi
    done < <(grep -oE "FROM +[^ ]+@sha256:[a-fA-F0-9]+" "${file}" 2>/dev/null | awk '{print $2}' || true)
done < <(find . -name "Dockerfile*" -not -path "*/node_modules/*" -not -path "*/target/*")

if [[ ${fail} -ne 0 ]]; then
    echo
    echo "Fix: resolve the real digest before committing, e.g."
    echo "  docker buildx imagetools inspect <image>:<tag> --format '{{.Manifest.Digest}}'"
    exit 1
fi

echo "OK: ${checked} pinned base image digests resolve (${skipped} skipped)."
