CREATE TABLE IF NOT EXISTS certificates (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL, -- ca | server | client
    issuer_id     TEXT REFERENCES certificates(id),
    common_name   TEXT NOT NULL,
    sans_json     TEXT NOT NULL DEFAULT '[]',
    key_algorithm TEXT NOT NULL,
    cert_pem      TEXT NOT NULL,
    key_pem       TEXT NOT NULL,
    not_before    TEXT NOT NULL,
    not_after     TEXT NOT NULL,
    created_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_certificates_kind ON certificates(kind);

-- Singleton row (id='default') describing the gateway's optional HTTPS
-- listener: which stored cert it presents, and whether it requires a
-- client cert signed by a stored CA.
CREATE TABLE IF NOT EXISTS gateway_tls_settings (
    id                  TEXT PRIMARY KEY DEFAULT 'default',
    enabled             INTEGER NOT NULL DEFAULT 0,
    cert_id             TEXT REFERENCES certificates(id),
    require_client_cert INTEGER NOT NULL DEFAULT 0,
    client_ca_id        TEXT REFERENCES certificates(id),
    updated_at          TEXT NOT NULL
);
