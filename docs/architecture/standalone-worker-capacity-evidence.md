# Standalone worker finite-capacity evidence

Source [b993b3300](https://github.com/zenjabba/silo-server/commit/b993b3300f528eb38cc64989d04b2f8fc8f4949a) passed four isolated Intel Flex 140 cases on 2026-10-14: HTTP and verified HTTPS, each with the worker CLI limit set to one or two jobs. All 243 checks passed. Eight complete outputs contain 44 TS segments and 2,640 decoded video frames. The [JSON report](standalone-worker-capacity-evidence.json) contains the source, binary, fixture, tooling, output and retained raw-result hashes.

| Transport | Worker limit | Case checks | Startup / seek frames | Concurrent capability responses | Jobs after seek | Cleanup |
| --- | ---: | ---: | ---: | --- | ---: | --- |
| HTTP | 1 | 59 | 420 / 240 | 200 / 200 | 1 | Passed |
| HTTP | 2 | 56 | 420 / 240 | 200 / 200 | 1 | Passed |
| HTTPS | 1 | 59 | 420 / 240 | 200 / 200 | 1 | Passed |
| HTTPS | 2 | 56 | 420 / 240 | 200 / 200 | 1 | Passed |

Thirteen additional checks cover isolation, the common binary, complete outputs and media parity. Each case uses the same synthetic 14-second source, VAAPI decode, QSV H.264 video encoding, and AAC audio. Workers have local scratch and the selected GPU, with no media-library mount or shared filesystem access. The planner's stored job limit is `NULL` so these cases exercise the actual worker CLI limit independently of planner admission.

Each case starts through the native API using a real login, reads existing output through the API-selected header-auth route and observes the core's private viewer authority. It retains the session for at least 65 seconds after an actual Go capability fetch, then makes two concurrent signed capability requests and seeks through the native replan route. The stored assignments show the same native-session `job_id` and distinct startup/seek work IDs. After commit, one worker job remains.

At limit one, the capability reads launch no FFmpeg process and an independent playback reaches the worker's 503 capacity refusal. At limit two, one retained session leaves a spare slot: the capability burst launches ten normal probe commands. Both paths complete startup and seek. The two-job GPU cases do not fill both slots; a focused tracker regression covers full-two refusal.

Startup outputs match across all four cases, as do seek outputs: complete segment hashes, decoded video/audio frame hashes and packet metadata hashes agree. Source Range access succeeds before stop. Signed stop releases the jobs and live assignments, the worker snapshot drops revoked work and viewers, and source and stale output access are refused. Network traces contain only the expected core origin and loopback connections.

All four task stacks stopped. The final independent host guard found no retained GPU owner, no new GPU kernel error, and no change to protected guests, device grants, boot, kernel, driver, mounts or taint state.

## Recorded failures

The JSON retains two earlier attempts on source `072a01f7b`. One failed in harness setup before launching a worker or encoder and produced no media; a separate independent check subsequently verified cleanup after correcting SSH argument quoting and path validation. The other produced all 420 startup frames and successful capability responses, then rejected native seek at the one-job limit because its new transport work ID counted as another job. It stopped before the remaining three cases and verified cleanup.

The correction carries a server-owned accounting identity in signed assignments and output authority while preserving separate work/generation permission checks. Permanent tests cover failed-candidate producer liveness, cold signed reconstruction, unrelated-job refusal, forged grouping fields, legacy identities and atomic Redis admission/completion. Independent Astra mutations reproduced failures when the signed ID, worker use, native caller context or completion preservation was removed. The prior attempts remain separate and source-pinned.

## Build identity and scope

The binary SHA-256 is `bd1ebe34e5b9002ff0b5ca880ac880ace5a409655a37cfc8aaf5885a7c7bb57a`. Go 1.26.4 built the verified clean commit with automatic VCS stamping disabled and explicit application revision/clean-state overrides. Source, existing web embed bytes and compiler inputs were hashed before and after the build; the primary agent independently checked them before releasing the GPU run. The evidence commit only adds documentation after that tested code commit.

This report validates finite-capacity behavior. The separate [source transport measurements](transcode-source-transport-evidence.md) and [runtime report](standalone-worker-runtime-evidence.md) retain their original source-pinned timing samples. These four cases add no latency, p95, WAN, cold-cache, HDR, sustained-load or production shared-storage claim. Deployment validation remains separate.
