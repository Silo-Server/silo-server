# Test fixtures

These are libmobi's own test sample files, vendored for converter unit tests.

Source: https://github.com/bfabiszewski/libmobi `tests/samples/` (LGPL-3.0; the
samples are project test data authored by the libmobi project).

| File | Purpose |
|------|---------|
| `sample-ncx.mobi`     | DRM-free KF8/MOBI6 hybrid — happy-path conversion |
| `sample-cp1252.mobi`  | DRM-free cp1252-encoded — second happy-path encoding |
| `sample-drm-v1.mobi`  | Encrypted source — exercises the DRM rejection path |
