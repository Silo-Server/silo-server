# Implemented standalone worker runtime evidence

Measured revision `37af64c486c2f6eb9e13e046a0b02e46e07c7106`; binary SHA256 `357a38084f321b6a832e12b9bc3cf29af8b44f26e83119508f7f36a1be2a5113`; source content SHA256 `19c3baadaec750e03e60ee9adf20eeabc84b60b34ab856aba14cf7ceb22a550b`. All four cohorts used this binary and source tree. Later upstream integration checks are recorded separately in [PR #2408](https://github.com/Silo-Server/silo-server/pull/2408).

The real core API drove existing legacy transcode and standalone worker executors on the same GPU and two CPU affinity cores. The standalone namespace contained its binding, public CA and local scratch; source and infrastructure credentials were absent. Source bytes traveled over HTTP or verified HTTPS. The GPU paired cohorts use a synthetic 1080p H.264/AAC fixture lasting 14 seconds and produce 720p H.264/AAC HLS at 1800 kbps target, 2000 kbps maximum and 4000 kbits buffer, with two-second keyframes.

|Transport|Action|Legacy median ms|Worker median ms|Median paired increase ms|Legacy range ms|Worker range ms|
|---|---|---:|---:|---:|---|---|
|HTTP|startup|768.1|1068.9|304.7|733.0–836.4|1053.8–1128.9|
|HTTP|seek|506.9|860.9|366.5|481.1–545.7|851.8–887.4|
|HTTPS|startup|788.3|1075.5|292.0|699.8–897.3|1056.3–1121.8|
|HTTPS|seek|395.3|811.9|409.1|388.6–407.7|797.7–881.1|

Five randomized paired rounds per protocol/action provide 40 timed operations. Readiness ends after fetching three complete TS bodies. Every startup verifies 420 frames; seeks request segments 3–5 and verify 240 remaining frames. Fresh paired outputs match in TS hashes, decoded video/audio framemd5 and packet metadata. Hardware decode/encode arguments and selected-device FDs were observed.

Most of the additional readiness latency appears before the playback API response. For example, HTTP startup API-response medians were 460.0 ms for legacy and 771.8 ms for standalone, while median response-to-readiness intervals were 274.5 and 291.1 ms. These observations do not identify the cost of individual grant, probe or setup steps; all four phase summaries are in the JSON.

Real software runtime checks: HTTP 43, HTTPS 45; all passed. Complete startup/restart output with 420 frames and seek output with 240 frames, authenticated source pause/backpressure and revocation, enrollment, restart reconstruction, replay denial, signed generation-scoped stop acknowledgment, zero active jobs, source/output revocation, source/secret isolation and HTTP-only egress were checked. Wrong TLS CA and hostname were rejected before HTTP. All three operator commands printed their real flags and exited 0 for `--help`. Public health reported `worker_protocol=http_only_v1`.

After timed operations, each protocol cold-rebuilt identical full GPU output following a same-binding worker restart, then revoked its source/output and released the job. Task processes/listeners/containers were closed and GPU/driver/guest state passed cleanup checks.

The test gateways close upstream and downstream HTTP connections after each ordinary response in both protocols and for both executors. Production Go clients support connection pooling. These results include the test gateways' connection policy; they do not isolate persistent-connection production latency.

Mode capability warmup and 2.25/17-second settling occur outside timing. Planner job caps are unlimited (SQL NULL, equivalent to API input 0) for this sequential pilot; the standalone operator limit is 2 and the legacy executor keeps its existing unlimited default. These samples provide medians and observed ranges, without p95 or capacity conclusions. Cache and shared-host scheduling are uncontrolled. NFS/SMB, WAN, HDR and maximum capacity are outside this evidence. The earlier raw-FFmpeg experiment uses a distinct 3600/4000/8000 kbps recipe; its n=30 and sustained-producer results remain separate. Sample, chapter, trickplay, subtitle, theme and prepared-download checks belong to a separate earlier feature cohort.

[Machine-readable samples, output digests, CLI flags and source fingerprint definition](standalone-worker-runtime-evidence.json).
