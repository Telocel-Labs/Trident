# Migration Recovery Guide: Duplicate Migrations 0033 & 0034

## Background
Due to concurrent feature branches, database migrations with conflicting versions were merged:
- `0033_per_network_cursor.sql` collided with `0033_audit_log_failed_auth_details.sql`
- `0034_webhook_delivery_idempotency_and_retry_state.sql` collided with `0034_partition_default_guard.sql`

These migrations were renumbered to ensure sequential uniqueness:
- `0033_per_network_cursor.sql` -> `0038_per_network_cursor.sql`
- `0034_webhook_delivery_idempotency_and_retry_state.sql` -> `0039_webhook_delivery_idempotency_and_retry_state.sql`

## Recovery Steps for Existing Environments

If your deployment environment applied migrations while they had colliding version numbers:

1. **Check applied migrations:**
   ```sql
   SELECT version, description, installed_on, success FROM _sqlx_migrations ORDER BY version;
   ```

2. **If `0033_per_network_cursor` was recorded under version `33`:**
   Since statement executions in `0038_per_network_cursor.sql` are guarded by `IF NOT EXISTS` / `ON CONFLICT DO NOTHING`, running:
   ```sql
   INSERT INTO _sqlx_migrations (version, description, success, checksum, execution_time)
   VALUES (38, 'per_network_cursor', true, '\x', 0)
   ON CONFLICT (version) DO NOTHING;
   ```
   will mark the renumbered migration as applied without running conflicting statements.

3. **If `0034_webhook_delivery_idempotency_and_retry_state` was recorded under version `34`:**
   Similarly:
   ```sql
   INSERT INTO _sqlx_migrations (version, description, success, checksum, execution_time)
   VALUES (39, 'webhook_delivery_idempotency_and_retry_state', true, '\x', 0)
   ON CONFLICT (version) DO NOTHING;
   ```

4. **Verify schema state:**
   Run `sqlx migrate run` to ensure all migrations apply cleanly to completion.
