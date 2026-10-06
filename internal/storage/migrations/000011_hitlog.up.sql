CREATE TABLE IF NOT EXISTS hit_logs (
    id                     TEXT PRIMARY KEY,
    mock_id                TEXT,
    protocol_type          TEXT NOT NULL,
    direction              TEXT NOT NULL, -- inbound | proxy-capture | outbound-client-call | callback
    method                 TEXT,
    path                   TEXT,
    target_url             TEXT,
    request_headers_json   TEXT NOT NULL DEFAULT '{}',
    request_body           TEXT,
    response_status        INTEGER,
    response_headers_json  TEXT NOT NULL DEFAULT '{}',
    response_body          TEXT,
    latency_ms             INTEGER NOT NULL DEFAULT 0,
    created_at             TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_hit_logs_mock_created ON hit_logs(mock_id, created_at);
CREATE INDEX IF NOT EXISTS idx_hit_logs_created ON hit_logs(created_at);
