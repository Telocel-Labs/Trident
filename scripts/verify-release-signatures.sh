#!/usr/bin/env bash
#
# scripts/verify-release-signatures.sh
# Verifies Cosign signatures, SLSA build provenance attestations, and SBOM attestations
# for Trident container release images (issue #780).
#
# Usage:
#   scripts/verify-release-signatures.sh --tag v0.2.0
#   scripts/verify-release-signatures.sh --image ghcr.io/telocel-labs/trident-indexer:v0.2.0
#   scripts/verify-release-signatures.sh --dry-run --tag v0.2.0
#

set -euo pipefail

DEFAULT_REPO="Telocel-Labs/Trident"
DEFAULT_REGISTRY="ghcr.io"
DEFAULT_IMAGES=("trident-indexer" "trident-grpc-api" "trident-go-api" "trident-migrate")

REPO="$DEFAULT_REPO"
REGISTRY="$DEFAULT_REGISTRY"
TAG=""
SPECIFIC_IMAGE=""
DRY_RUN=false
SKIP_COSIGN=false
SKIP_ATTESTATION=false

COSIGN_CMD="${COSIGN_BIN:-cosign}"
GH_CMD="${GH_BIN:-gh}"

print_usage() {
    cat <<'EOF'
Usage: verify-release-signatures.sh [OPTIONS] [IMAGE]

Verifies Cosign keyless signatures and GitHub SLSA/SBOM attestations for Trident images.

Options:
  -t, --tag <tag>             Release tag to verify (e.g., v0.2.0).
  -r, --repo <owner/repo>     GitHub repository (default: Telocel-Labs/Trident).
      --registry <host>       Container registry (default: ghcr.io).
  -i, --image <image-ref>     Specific container image reference to verify.
      --dry-run               Print verification commands without executing them.
      --skip-cosign           Skip Cosign signature verification.
      --skip-attestation      Skip GitHub CLI attestation verification.
  -h, --help                  Show this help message.

Examples:
  # Verify all release images for v0.2.0
  scripts/verify-release-signatures.sh --tag v0.2.0

  # Verify a single specific image
  scripts/verify-release-signatures.sh --image ghcr.io/telocel-labs/trident-indexer:v0.2.0

  # Dry-run inspection of verification commands
  scripts/verify-release-signatures.sh --tag v0.2.0 --dry-run
EOF
}

# Parse options
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            print_usage
            exit 0
            ;;
        -t|--tag)
            TAG="$2"
            shift 2
            ;;
        -r|--repo)
            REPO="$2"
            shift 2
            ;;
        --registry)
            REGISTRY="$2"
            shift 2
            ;;
        -i|--image)
            SPECIFIC_IMAGE="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --skip-cosign)
            SKIP_COSIGN=true
            shift
            ;;
        --skip-attestation)
            SKIP_ATTESTATION=true
            shift
            ;;
        *)
            if [[ -z "$SPECIFIC_IMAGE" && "$1" != -* ]]; then
                SPECIFIC_IMAGE="$1"
                shift
            else
                echo "error: unknown argument: $1" >&2
                print_usage >&2
                exit 1
            fi
            ;;
    esac
done

if [[ -z "$TAG" && -z "$SPECIFIC_IMAGE" ]]; then
    echo "error: either --tag <tag> or --image <image-ref> must be provided." >&2
    print_usage >&2
    exit 1
fi

OWNER="${REPO%%/*}"

# Pre-flight tool check if not in dry-run mode
if [[ "$DRY_RUN" != "true" ]]; then
    if [[ "$SKIP_COSIGN" != "true" ]] && ! command -v "$COSIGN_CMD" >/dev/null 2>&1; then
        echo "error: cosign executable not found ($COSIGN_CMD). Install cosign or specify --skip-cosign." >&2
        exit 2
    fi
    if [[ "$SKIP_ATTESTATION" != "true" ]] && ! command -v "$GH_CMD" >/dev/null 2>&1; then
        echo "error: gh executable not found ($GH_CMD). Install GitHub CLI or specify --skip-attestation." >&2
        exit 2
    fi
fi

TARGET_IMAGES=()
if [[ -n "$SPECIFIC_IMAGE" ]]; then
    TARGET_IMAGES+=("$SPECIFIC_IMAGE")
else
    for name in "${DEFAULT_IMAGES[@]}"; do
        TARGET_IMAGES+=("${REGISTRY}/${OWNER,,}/${name}:${TAG}")
    done
fi

echo "=========================================================="
echo "Trident Release Supply Chain Verification"
echo "Repository: $REPO"
echo "Target count: ${#TARGET_IMAGES[@]}"
echo "Dry run: $DRY_RUN"
echo "=========================================================="

FAILED=0

for img in "${TARGET_IMAGES[@]}"; do
    echo
    echo "==> Verifying image: $img"
    
    # Extract tag if needed for regex check
    CURRENT_TAG="$TAG"
    if [[ -z "$CURRENT_TAG" && "$img" == *":"* ]]; then
        CURRENT_TAG="${img##*:}"
    fi

    # 1. Cosign Signature Verification
    if [[ "$SKIP_COSIGN" != "true" ]]; then
        echo "  [1/2] Verifying Cosign keyless signature..."
        
        IDENTITY_REGEX="^https://github.com/${REPO}/\.github/workflows/release\.yml@refs/tags/${CURRENT_TAG}$"
        OIDC_ISSUER="https://token.actions.githubusercontent.com"
        
        COSIGN_ARGS=(
            "verify"
            "--certificate-identity-regexp" "$IDENTITY_REGEX"
            "--certificate-oidc-issuer" "$OIDC_ISSUER"
            "$img"
        )
        
        if [[ "$DRY_RUN" == "true" ]]; then
            echo "    [dry-run] $COSIGN_CMD ${COSIGN_ARGS[*]}"
        else
            if "$COSIGN_CMD" "${COSIGN_ARGS[@]}"; then
                echo "    ✓ Cosign signature valid and matches workflow identity"
            else
                echo "    ✗ Cosign verification FAILED for $img" >&2
                FAILED=$((FAILED + 1))
            fi
        fi
    fi

    # 2. GitHub CLI Attestation Verification (SLSA & SBOM)
    if [[ "$SKIP_ATTESTATION" != "true" ]]; then
        echo "  [2/2] Verifying SLSA build provenance and SBOM attestations..."
        
        OCI_REF="oci://$img"
        GH_ARGS=(
            "attestation"
            "verify"
            "$OCI_REF"
            "--owner" "$OWNER"
        )
        
        if [[ "$DRY_RUN" == "true" ]]; then
            echo "    [dry-run] $GH_CMD ${GH_ARGS[*]}"
        else
            if "$GH_CMD" "${GH_ARGS[@]}"; then
                echo "    ✓ Build attestations (SLSA / SBOM) verified"
            else
                echo "    ✗ Attestation verification FAILED for $img" >&2
                FAILED=$((FAILED + 1))
            fi
        fi
    fi
done

echo
echo "=========================================================="
if [[ "$FAILED" -eq 0 ]]; then
    echo "Summary: All supply chain verifications PASSED."
    echo "=========================================================="
    exit 0
else
    echo "Summary: $FAILED verification(s) FAILED." >&2
    echo "=========================================================="
    exit 1
fi
