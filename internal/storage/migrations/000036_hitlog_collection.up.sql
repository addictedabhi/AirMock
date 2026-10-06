-- Lets a DirectionOutboundCall hit_logs row record which API-client
-- Collection (and which request within it) triggered it, so the Dashboard
-- can rank collections by traffic the same way it already ranks mocks.
ALTER TABLE hit_logs ADD COLUMN collection_id TEXT NOT NULL DEFAULT '';
ALTER TABLE hit_logs ADD COLUMN collection_name TEXT NOT NULL DEFAULT '';
ALTER TABLE hit_logs ADD COLUMN request_name TEXT NOT NULL DEFAULT '';
