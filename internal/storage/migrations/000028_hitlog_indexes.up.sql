-- Log History's filter bar and every /metrics scrape's MetricsSnapshot
-- query (WHERE direction = ? AND mock_id IS NOT NULL) previously had no
-- index to use beyond (mock_id, created_at)/(created_at) alone, forcing a
-- full table scan on every such query once hit_logs grows large.
CREATE INDEX IF NOT EXISTS idx_hit_logs_direction_created ON hit_logs(direction, created_at);
CREATE INDEX IF NOT EXISTS idx_hit_logs_protocol_created ON hit_logs(protocol_type, created_at);
