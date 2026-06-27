.PHONY: lint-openapi lint-sdk check-sdk-types help

help:
	@echo "Available targets:"
	@echo "  lint-openapi      - Lint OpenAPI spec with Spectral"
	@echo "  generate-sdk-types - Generate SDK types from OpenAPI spec"
	@echo "  check-sdk-types   - Verify SDK types are up-to-date"
	@echo "  lint-sdk          - Lint SDK TypeScript"

# Lint OpenAPI spec with Spectral
lint-openapi:
	@command -v spectral >/dev/null 2>&1 || { echo "spectral not found. Install with: npm install -g @stoplight/spectral-cli"; exit 1; }
	spectral lint api/openapi.yaml --ruleset @stoplight/spectral-oas

# Generate SDK types from OpenAPI spec
generate-sdk-types:
	cd sdk/typescript && npm run generate:types

# Check if SDK types are up-to-date (fails if stale)
check-sdk-types:
	@cd sdk/typescript && \
	npm run generate:types && \
	if git diff --quiet src/api-types.gen.ts; then \
		echo "✓ SDK types are up-to-date"; \
	else \
		echo "✗ SDK types are stale. Run 'make generate-sdk-types' and commit."; \
		git diff src/api-types.gen.ts; \
		exit 1; \
	fi

# Lint SDK
lint-sdk:
	cd sdk/typescript && npm run lint

.PHONY: ci-openapi
ci-openapi: lint-openapi check-sdk-types
	@echo "✓ OpenAPI spec and SDK types validated"
