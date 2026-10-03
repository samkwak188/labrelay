CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS tokens(hash text PRIMARY KEY, project text NOT NULL, access text NOT NULL CHECK(access IN ('read','write')), revoked boolean NOT NULL DEFAULT false);
CREATE TABLE IF NOT EXISTS settings(key text PRIMARY KEY,value text NOT NULL);
INSERT INTO settings VALUES('maintenance','false') ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS ingestions(
 id text PRIMARY KEY, project text NOT NULL, name text NOT NULL, digest text NOT NULL,
 manifest bytea NOT NULL, size_bytes bigint NOT NULL, state text NOT NULL CHECK(state IN ('STAGING','VERIFYING','READY','FAILED','EXPIRED')),
 finalize_requested boolean NOT NULL DEFAULT false, error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days',
 published_at timestamptz, revision_id text UNIQUE
);
CREATE UNIQUE INDEX IF NOT EXISTS live_revision ON ingestions(project,name,digest) WHERE state IN ('STAGING','VERIFYING','READY');
CREATE TABLE IF NOT EXISTS files(
 id text PRIMARY KEY, ingestion_id text NOT NULL REFERENCES ingestions(id),path text NOT NULL,role text NOT NULL,
 size_bytes bigint NOT NULL,sha256 text NOT NULL,object_key text,version_id text,verified_at timestamptz,
 UNIQUE(ingestion_id,path)
);
CREATE TABLE IF NOT EXISTS attempts(
 id text PRIMARY KEY,file_id text NOT NULL REFERENCES files(id),object_key text NOT NULL UNIQUE,upload_id text UNIQUE,
 state text NOT NULL CHECK(state IN ('CREATING','ACTIVE','VERIFIED','TOMBSTONED','CLEANED')),
 created_at timestamptz NOT NULL DEFAULT now(),error text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS active_file_attempt ON attempts(file_id) WHERE state IN ('CREATING','ACTIVE','VERIFIED');
CREATE TABLE IF NOT EXISTS events(id bigserial PRIMARY KEY,ingestion_id text NOT NULL REFERENCES ingestions(id),kind text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),detail text NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS ingest_pending ON ingestions(state,created_at);
CREATE INDEX IF NOT EXISTS attempt_cleanup ON attempts(state);
INSERT INTO schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;
