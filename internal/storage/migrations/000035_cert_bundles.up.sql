-- A Bundle names a related CA + server cert + client cert together (the
-- output of the "Generate certificate" quick-flow, which already creates
-- exactly these three, plus lets one be assembled from pre-existing certs).
-- Applying a bundle to a mock/gateway/project resolves its server_cert_id
-- and ca_id in one action instead of picking each cert individually.
CREATE TABLE IF NOT EXISTS cert_bundles (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    ca_id           TEXT REFERENCES certificates(id),
    server_cert_id  TEXT REFERENCES certificates(id),
    client_cert_id  TEXT REFERENCES certificates(id),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cert_bundles_name ON cert_bundles(lower(name));

-- client_cert_mode replaces the plain require_client_cert bool with a
-- tri-state ("" / "none" | "optional" | "required") — additive column, old
-- rows read as "" and fall back to require_client_cert for compatibility
-- (see GetGatewaySettings).
ALTER TABLE gateway_tls_settings ADD COLUMN client_cert_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE gateway_tls_settings ADD COLUMN bundle_id TEXT REFERENCES cert_bundles(id);
