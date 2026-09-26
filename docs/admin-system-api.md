# Administrator system inspection

`GET /api/v2/admin/system/build` reports the running binary's build identity.
Build metadata is diagnostic; clients detect features through capability documents.
Unknown build timestamps are omitted. Present timestamps use UTC with millisecond
precision. The build number remains a numeric counter.

`GET /api/v2/admin/system/resources` reads the latest published resource sample of
the API host serving the request. It does not probe hardware, wait for mounts, or
query stream workers. In a cluster, successive requests can describe different API
hosts. An absent sampler or an unsupported host reports `available: false`; clients
must not interpret this as zero utilization. A present `sampled_at` identifies the
age of the sample. Missing GPU measurements remain omitted, while a measured zero
remains zero. GPU and disk collections are arrays, including when empty.

Both operations require an acting administrator. Resource disk paths are available
only behind this gate. Responses use the native API's default no-store policy.
The frozen v1 inspection operations retain their existing representation.

The web build display and API-host resource panel consume these v2 operations.
The migration inventory identifies no Apple or Android consumers for these two
operations. They are native administrator diagnostics; Jellyfin compatibility
behavior does not change.
