-- +goose Up
-- Artwork storage used to be recorded three ways: the backend name from the
-- first write, an S3 fingerprint the reconcile task certified, and a delivery
-- scope on each revision. One row now names where the catalog's artwork keys
-- live, in the form the store reports: "local|<root>" or
-- "s3|<endpoint>|<bucket>|<prefix>". Existing installs keep their recorded
-- storage by translating the S3 fingerprint every earlier release wrote; the
-- backend row only ever existed on unreleased builds, so its absence means S3.
-- Deployments that never recorded storage record it on their next write.
INSERT INTO server_settings (key, value)
SELECT 'artwork.storage_identity',
       CASE
           WHEN identity.value LIKE 'local|%' THEN identity.value
           WHEN identity.value <> '' AND coalesce(active.value, 's3') = 's3' THEN 's3|' || identity.value
           ELSE ''
       END
FROM (SELECT value FROM server_settings WHERE key = 's3.public_storage_identity') identity
LEFT JOIN (SELECT value FROM server_settings WHERE key = 'artwork.storage_backend_active') active ON TRUE
WHERE NOT EXISTS (SELECT 1 FROM server_settings WHERE key = 'artwork.storage_identity');
-- An empty value is treated as absent by the settings store.
DELETE FROM server_settings WHERE key = 'artwork.storage_identity' AND value = '';
DELETE FROM server_settings
WHERE key IN ('artwork.storage_backend_active', 's3.public_storage_identity', 's3.public_storage_sweep_checkpoint');

-- +goose Down
-- Give the previous release back the S3 fingerprint it wrote, so a rolled-back
-- server still detects a changed bucket instead of seeding the next configured
-- location as authoritative. A local identity has no legacy form; the earlier
-- release ignores it.
INSERT INTO server_settings (key, value)
SELECT 's3.public_storage_identity', substr(value, length('s3|') + 1)
FROM server_settings
WHERE key = 'artwork.storage_identity' AND value LIKE 's3|%'
  AND NOT EXISTS (SELECT 1 FROM server_settings WHERE key = 's3.public_storage_identity');
DELETE FROM server_settings WHERE key = 'artwork.storage_identity';
