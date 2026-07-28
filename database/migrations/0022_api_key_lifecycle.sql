-- API key lifecycle: rotation, expiry, and scoped permissions (issue #314).
--
-- Adds four columns to api_keys:
--   expires_at   optional hard expiry, enforced in the auth query alongside
--                revoked_at.
--   scope        'read' or 'admin' — lets ordinary database-issued keys be
--                distinguished for per-route enforcement (see
--                middleware.RequireScope), separate from the existing
--                ADMIN_API_KEY env-var gate on the /v1/api-keys* and
--                /v1/admin/* management endpoints, which is unchanged.
--   grace_until  set by POST /v1/api-keys/{id}/rotate on the OLD key being
--                replaced. While grace_until is in the future the old key
--                keeps authenticating (so callers have a rollover window);
--                once it has passed, the auth query's own WHERE clause
--                excludes the key and a lazy UPDATE in that same query sets
--                revoked_at, so the old key is deterministically retired
--                without a background cron.
--   rotated_from / rotated_to  link the old and new rows of a rotation for
--                audit purposes. The old row is kept (not deleted) so usage
--                history and audit_log foreign keys remain intact.

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS expires_at  TIMESTAMPTZ;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope       TEXT NOT NULL DEFAULT 'read';
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS grace_until TIMESTAMPTZ;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS rotated_from UUID REFERENCES api_keys(id);
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS rotated_to   UUID REFERENCES api_keys(id);

-- Backward compatibility: before this migration every valid key had
-- full (admin-equivalent) power on every route — there was no scoping.
-- Silently defaulting every pre-existing key to 'read' would downgrade
-- whatever they are currently used for, so backfill existing rows to
-- 'admin' explicitly. Only keys created after this migration via
-- CreateAPIKey default to the new, safer 'read' scope.
UPDATE api_keys SET scope = 'admin' WHERE scope = 'read';

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS chk_api_keys_scope;
ALTER TABLE api_keys ADD CONSTRAINT chk_api_keys_scope CHECK (scope IN ('read', 'admin'));

-- Partial index so the auth path's expiry/grace check stays index-friendly.
CREATE INDEX IF NOT EXISTS idx_api_keys_expires_at ON api_keys (expires_at)
    WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_api_keys_grace_until ON api_keys (grace_until)
    WHERE grace_until IS NOT NULL;
