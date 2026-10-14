# Post-merge standalone worker integration acceptance

Captured commit `e1748f5edb374f11abee164e311a82c6687b6eb6`; binary SHA256 `18e736174ae88575622626315848549aadf58f9efd3bc76ae38b407675d63d3b`.

Source fingerprint `db31138ab7e037877b3f7a03aa925bcbc6a314741e78df599adb5a47748555d6` (3123 files); embedded artifacts fingerprint `d9842ca055ec0517c568207848b78ec14bf9428d8155a5efc4d66eb557b3ae71` (1903 files). Exact input sets and hash definitions are in the companion JSON.

Both fresh protocols passed the complete actual software runtime acceptance, including native and legacy-client full output, same-binding cold reconstruction, seek, source backpressure/revocation, enrollment, replay denial, isolation, HTTP-only worker egress, real CLI help, and cleanup. Verified HTTPS rejected wrong CA and hostname before HTTP.

- HTTP: 66 checks; seven saved full-media outputs and 46 TS; all passed.
- HTTPS: 68 checks; seven saved full-media outputs and 46 TS; all passed.

Two added sessions per protocol exercised sequenced administrator fallback stop and realtime-completed stop. Each verified full 420-frame media, one signed exact-work/generation executor 204, zero active jobs, source/output denial and exactly one history record. The WebSocket is the existing authenticated client-to-core session API; worker communication remains HTTP/HTTPS only. Pause checks cover dispatch/receipt and submitted acknowledgement/result, without claiming confirmed pause-state read-back.

Test gateways used bounded per-connection TLS handshakes and forced `Connection: close` in each direction. These functional checks do not measure pooled-client performance. No GPU workload or performance measurement was repeated. The earlier measured four-cohort 40-operation evidence remains tied to its original binary/source fingerprints. This report is separate integration acceptance after the upstream merge.

[Compact provenance, media digests and acceptance checks](standalone-worker-integration-evidence.json).
