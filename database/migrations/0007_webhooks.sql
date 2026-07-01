CREATE TABLE IF NOT EXISTS webhook_subscriptions (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  api_key_id   UUID        NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  contract_id  TEXT        NOT NULL,
  topic0       TEXT,
  target_url   TEXT        NOT NULL,
  secret       TEXT        NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  paused_at    TIMESTAMPTZ,
  network      TEXT        NOT NULL DEFAULT 'testnet'
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id              BIGSERIAL   PRIMARY KEY,
  subscription_id UUID        NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
  event_id        UUID        NOT NULL REFERENCES soroban_events(id),
  attempt         INT         NOT NULL DEFAULT 1,
  status_code     INT,
  response_body   TEXT,
  delivered_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  success         BOOLEAN     NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_webhook_subscriptions_contract_id ON webhook_subscriptions (contract_id);
CREATE INDEX IF NOT EXISTS idx_webhook_subscriptions_paused_at ON webhook_subscriptions (paused_at);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_subscription_id ON webhook_deliveries (subscription_id);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_delivered_at ON webhook_deliveries (delivered_at);
