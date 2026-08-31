# 📦 Trident SDK Versioning and Release Policy

This document defines the formal **versioning scheme, release workflows, changelog standards, and breaking change propagation lifecycle** across all official Trident client SDKs:

- **TypeScript SDK** (`@trident/sdk` on [npm](https://www.npmjs.com))
- **React SDK** (`@trident/react` on [npm](https://www.npmjs.com))
- **Python SDK** (`trident-sdk` on [PyPI](https://pypi.org))
- **Rust SDK** (`trident-sdk` on [crates.io](https://crates.io))
- **Go SDK** (`github.com/Telocel-Labs/Trident/sdk/go` on [Go Modules](https://pkg.go.dev))

---

## 1. Versioning Scheme & API Alignment

All official Trident SDKs adhere strictly to **Semantic Versioning 2.0.0** (`MAJOR.MINOR.PATCH`).

```
           v [MAJOR] . [MINOR] . [PATCH]
                │         │         │
                │         │         └─► Bug fixes & performance patches (backward-compatible)
                │         └───────────► New client methods, fields, filters (backward-compatible)
                └─────────────────────► Breaking SDK changes or API Major Version alignment
```

### Relationship with Trident API Versions

| Component | Version Sync Rule | Example |
|---|---|---|
| **API `v1`** | SDKs remain in `1.x.y` series | `v1.2.0` SDK queries `/v1/*` API endpoints |
| **API `v2`** | SDKs bump to `2.0.0` series | `v2.0.0` SDK queries `/v2/*` API endpoints |

1. **`MAJOR` (Breaking Changes)**:
   - Incremented when the underlying Trident API introduces a breaking change (e.g. endpoint deprecation, schema restructuring).
   - Incremented when SDK method signatures, configuration types, or required runtime environments break backward compatibility.
2. **`MINOR` (New Capabilities)**:
   - Incremented when new backward-compatible features are added (e.g. support for new Soroban event types, new query helpers, WebSocket reconnection policies).
3. **`PATCH` (Bug & Reliability Fixes)**:
   - Incremented for backward-compatible bug fixes, dependency security updates, and performance enhancements.

---

## 2. Release Process Per Ecosystem

Each SDK is published to its canonical package manager following automated CI/CD tag gates:

```
┌─────────────────────────────────────────────────────────────┐
│                      Release Trigger                        │
│             git tag push: sdk/<ecosystem>/vX.Y.Z            │
└─────────────────────────────────────────────────────────────┘
                               │
       ┌───────────────────────┼───────────────────────┐
       ▼                       ▼                       ▼
 ┌───────────┐           ┌───────────┐           ┌───────────┐
 │    npm    │           │   PyPI    │           │ crates.io │
 │ (TS/React)│           │ (Python)  │           │  (Rust)   │
 └───────────┘           └───────────┘           └───────────┘
```

### 2.1 TypeScript (`@trident/sdk`) & React (`@trident/react`)

- **Registry**: npm
- **Release Tag Pattern**: `sdk/typescript/v1.0.0` / `sdk/react/v1.0.0`
- **Release Steps**:
  ```bash
  cd sdk/typescript
  npm ci
  npm run test
  npm run build
  npm publish --access public --provenance
  ```

### 2.2 Python (`trident-sdk`)

- **Registry**: PyPI
- **Release Tag Pattern**: `sdk/python/v1.0.0`
- **Release Steps**:
  ```bash
  cd sdk/python
  python3 -m pip install --upgrade build twine
  python3 -m build
  twine check dist/*
  twine upload dist/*
  ```

### 2.3 Rust (`trident-sdk`)

- **Registry**: crates.io
- **Release Tag Pattern**: `sdk/rust/v1.0.0`
- **Release Steps**:
  ```bash
  cd sdk/rust
  cargo test --all-features
  cargo package --allow-dirty
  cargo publish
  ```

### 2.4 Go (`github.com/Telocel-Labs/Trident/sdk/go`)

- **Registry**: Go Module Proxy (`proxy.golang.org`)
- **Release Tag Pattern**: `sdk/go/v1.0.0` (Semantic Submodule Tagging)
- **Release Steps**:
  ```bash
  # Go modules inside monorepos require the submodule path prefix:
  git tag sdk/go/v1.0.0
  git push origin sdk/go/v1.0.0
  # Proxy warm-up:
  GOPROXY=https://proxy.golang.org go list -m github.com/Telocel-Labs/Trident/sdk/go@v1.0.0
  ```

---

## 3. Changelog Expectations

Every SDK release **must** update its corresponding `CHANGELOG.md` following [Keep a Changelog 1.0.0](https://keepachangelog.com/en/1.0.0/):

```markdown
## [1.2.0] - 2026-08-29

### Added
- Added `streamEventsByContract` WebSocket client subscription helper (#196).
- Added `network_passphrase` configuration option for custom Stellar networks.

### Changed
- Improved exponential backoff jitter calculation during RPC 429 retries.

### Fixed
- Fixed memory leak in long-lived SSE event listeners (#214).

### Security
- Updated transitive dependencies to patch libvips advisory.
```

### Standard Changelog Sections
- `Added`: for new features.
- `Changed`: for changes in existing functionality.
- `Deprecated`: for soon-to-be removed features.
- `Removed`: for now removed features.
- `Fixed`: for any bug fixes.
- `Security`: in case of vulnerabilities.

---

## 4. Breaking API Change Propagation Lifecycle

When the Trident API introduces a breaking change, the SDKs follow a **staged 3-phase deprecation lifecycle**:

```
[Phase 1: Deprecation Notice] ──► [Phase 2: Migration Period (6 Months)] ──► [Phase 3: Hard Removal]
      (Runtime Warnings)                    (Dual Support)                      (SDK Major Bump)
```

1. **Phase 1 — Deprecation Announcement**:
   - The old API endpoint/field is marked as `@deprecated` in SDK type definitions with compiler and runtime warnings pointing to the new alternative.
2. **Phase 2 — Dual Compatibility (Minimum 6 Months)**:
   - The SDK supports both legacy and new API formats simultaneously.
   - Detailed migration guides published in `docs/migrations/`.
3. **Phase 3 — Major Version Release**:
   - The SDK increments its `MAJOR` version (`v1.x.x` -> `v2.0.0`).
   - Deprecated methods and types are removed.

---

## 5. Security & Hotfix Protocol

- **Critical Vulnerabilities (CVSS >= 7.0)**: Patched across all actively supported SDK major versions within **48 hours**.
- **Patch Releases**: Published immediately to package registries with the `PATCH` version incremented.
