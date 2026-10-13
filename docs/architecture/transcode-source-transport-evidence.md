# Transcode source transport evidence

On 2026-10-13, direct FFmpeg jobs on an Intel Data Center GPU Flex 140 measured
the cost of reading the same synthetic source from a local file, HTTP, and
verified HTTPS on a test LAN. Median within-round readiness overhead was
5.82 ms for HTTP and 12.33 ms for HTTPS at startup, and 9.98 ms and 15.55 ms
for a fresh seek. Longer producer jobs retained 98.69% and 96.71% of the
local-file rate, respectively.

These observations support HTTP/HTTPS source transport for the
[standalone worker](standalone-transcode-workers.md). They measure direct packaged FFmpeg execution, not a
complete standalone Silo worker or its control plane. The retained worker
contracts are described in [worker-http-protocol.md](worker-http-protocol.md).

## Readiness

Each action had 30 paired rounds, with the three transport modes shuffled in
each round using seed `20261013`: 180 primary jobs in total. Every job produced
8 seconds of output, 240 video frames and four 2-second MPEG-TS segments.
Startup begins at source time zero. Seek launches a fresh FFmpeg process with
input `-ss 30`; it does not measure a running Silo session's seek/restart path.

| Action | Transport | Jobs | Readiness median ms | Readiness p95 ms |
| --- | --- | ---: | ---: | ---: |
| Startup | File | 30 | 380.05 | 394.95 |
| Startup | HTTP | 30 | 389.85 | 399.47 |
| Startup | HTTPS | 30 | 391.41 | 409.77 |
| Seek | File | 30 | 390.13 | 402.13 |
| Seek | HTTP | 30 | 398.32 | 414.72 |
| Seek | HTTPS | 30 | 405.80 | 416.74 |

Readiness is measured from immediately before the FFmpeg process is forked
until its playlist references at least three published, nonempty segments.
The monotonic timer polls every 5 ms. Timing includes process startup, probing
and encoding; it excludes SSH dispatch, namespace setup, image export and
fixture transfer. This is a producer readiness measurement rather than
end-to-end player startup latency.

Medians use the conventional median, averaging the center pair for even sample
counts. p95 uses nearest rank: `sorted[ceil(0.95*n)-1]`. At 30 samples, it is
descriptive. Paired overhead is the median of network-minus-file differences
within each action and round; it differs from subtracting the table's group
medians.

## Producer rate and concurrent correctness

| Transport | Jobs | Active output FPS median | Percentage of file rate |
| --- | ---: | ---: | ---: |
| File | 3 | 761.214 | 100.00% |
| HTTP | 3 | 751.229 | 98.69% |
| HTTPS | 3 | 736.152 | 96.71% |

Each producer job encoded 240 seconds and 7,200 frames. FPS is the least-squares
slope of frame count against monotonic elapsed time for sampled progress whose
frame count lies between 600 and 6,600 inclusive, corresponding to the
20–220 second content window. The observed windows lasted 7.81–8.13 wall
seconds and contained 79–82 samples. These nine jobs measure active production
after startup, with three repeats per mode; they do not establish thermal-soak
performance or sustained stream capacity.

Two- and four-stream bursts ran twice per transport: 12 batches containing
36 jobs, plus three single-stream reference jobs. Each job produced 24 seconds,
720 frames and 12 segments. Every burst matched its reference output exactly.
All jobs shared the same two CPU affinity cores at nice level 10; the brief
bursts show correctness under contention, without establishing maximum capacity.
Producer and burst transport order used seed `20261253`.

## Provenance and recipe

The runtime came from a new stopped container created from an immutable image.
It contained FFmpeg `8.1.3-Jellyfin`, oneVPL `2.17`, Intel media runtime `1.2.17`
and iHD `26.3.5`. The selected GPU used i915 driver `25.2.57-3` on Linux
`6.17.13-21-pve`; loaded and installed driver identities matched. Every
transport used the same GPU, userspace and CPU affinity. The JSON records
content hashes for the runtime files, timer, source fixtures and original
captures.

The recipe follows Silo commit
[`48dc950ee3f782a2d552ce89c7a61644543745d1`](https://github.com/Silo-Server/silo-server/commit/48dc950ee3f782a2d552ce89c7a61644543745d1).
Its relevant builders were also checked at upstream commit
[`4b2e921c5a70c6a1dde61ca24e3b2b07e3ccdc22`](https://github.com/Silo-Server/silo-server/commit/4b2e921c5a70c6a1dde61ca24e3b2b07e3ccdc22):
VAAPI H264 decode, `scale_vaapi` to 720p/NV12, surfaces mapped to derived QSV,
and `h264_qsv` encoding with preset `veryfast`. Video used a 3,600 kbps target,
4,000 kbps ceiling and 8,000 kbps buffer, with 60-frame GOP/keyint, forced IDRs
and 2-second keyframe boundaries. Audio was AAC at 128 kbps, stereo, 48 kHz.
Output was independent MPEG-TS HLS with atomic temporary segment files.

The synthetic input was faststart MP4 containing 1920×1080, 30 FPS H264 and a
48 kHz sine-wave AAC track, approximately 12.2 Mbps combined. FFmpeg `6.1.1`
generated a 48-second fixture; a stream-copy repetition produced the
240-second fixture. The JSON preserves the generator and transcode options.
Reproduction with another encoder build requires fresh output calibration.

HTTPS explicitly used `-tcp_nodelay 1`, `-tls_verify 1`, a public task CA and
`-verifyhost` for the actual certificate identity. Wrong-CA and wrong-identity
controls failed before any HTTP source-body bytes. HTTP/HTTPS client mount
namespaces had no source fixture or CA private key. This does not establish
credential-free Silo bootstrap or runtime authorization.

The finite recipes normalize the test timeline and omit production `copyts`
and `avoid_negative_ts` options. They also omit Silo's dynamic throttle and
bounded playlist policy. Hardware decode/encode was checked through verbose
surface/runtime logs and selected-device file descriptors; GPU utilization
percentages were unavailable.

## Correctness and audit data

Equivalent jobs had identical segment SHA256 hashes across file repetitions
and transports. Calibration matched decoded video/audio frame digests, packet
metadata and stream identity. Representative producer and burst proofs decoded
7,200 and 720 video frames per transport, respectively. Packet metadata
digests cover timing, sizes and flags; they are not compressed packet-payload
hashes.

Independent verification checked recipes, counts, statistics, proofs and saved
outputs. A final rehash covered 273 saved task records, including diagnostics,
and 2,420 TS files totaling 2,514,142,800 bytes, with no mismatches. That audit
count is separate from the 180 primary timing jobs. Cleanup confirmed all eight
captured source ports closed, the selected GPU idle, unchanged host mounts and
driver state, and existing workloads still running.

[The JSON sample appendix](transcode-source-transport-evidence.samples.md) contains
allowlisted per-run timing samples, round order, producer progress samples,
summary statistics, recipe/provenance fields and correctness digests. Readiness
and elapsed samples are in milliseconds; producer progress entries are
`[elapsed_seconds, frame]`. Readers can recompute medians, nearest-rank p95,
within-round differences and producer slopes directly from these fields.

The local-file baseline is synthetic host tmpfs. NFS/SMB storage, real WAN,
cold-cache behavior, HDR, real-library workloads, the Go adapter,
full Silo boot/auth/control/reconstruction/output-grant behavior and maximum
sustained capacity remain unmeasured. The findings support source-transport
feasibility and provide a comparison for the eventual implementation; they
are not a complete runtime performance acceptance result.
