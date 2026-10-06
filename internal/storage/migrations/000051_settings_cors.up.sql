ALTER TABLE app_settings ADD COLUMN cors_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE app_settings ADD COLUMN cors_allow_origin TEXT NOT NULL DEFAULT '*';
ALTER TABLE app_settings ADD COLUMN cors_allow_methods TEXT NOT NULL DEFAULT 'GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS';
ALTER TABLE app_settings ADD COLUMN cors_allow_headers TEXT NOT NULL DEFAULT '*';
ALTER TABLE app_settings ADD COLUMN cors_allow_credentials INTEGER NOT NULL DEFAULT 0;
ALTER TABLE app_settings ADD COLUMN cors_max_age_secs INTEGER NOT NULL DEFAULT 600;
