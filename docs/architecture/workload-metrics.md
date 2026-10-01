# Workload metrics

`internal/workmetrics` records process-local execution attempts. One scheduled
task can initiate several durable jobs, so attempts are not counts of media
items. Compare the same boundary over time; do not add parent task durations to
child-job durations. Metric dimensions use a finite vocabulary; unknown task
keys fold to `other`, and dynamic plugin tasks fold to `plugin`.

`active` is incremented on admission and decremented once on exit. Durations
include the whole attempt. Outcomes are `success`, `error`, `canceled`, `timeout`
and `unknown`. Unknown means the owner could not establish a terminal outcome;
it must not be treated as success. Recovery counters count committed stale-claim
recoveries, not heartbeat requests. Progress counters exclude heartbeat loops.

## Queue and worker catalog

| Work | Authoritative state and unit | Instrumentation / retry and stall interpretation |
| --- | --- | --- |
| Library scan | `scan_runs`, one folder/subtree/file attempt | Scan worker start/progress/durable terminal outcome. Shared queued/running counts and request age. Stale direct attempts fail; stale worker claims requeue. |
| Autoscan | `autoscan_webhook_deliveries`, one delivery; `scan_runs` for admitted scans | Scheduled poll/retry task attempts and intake queue depth/age. Delivery rows can fan out to several scans; count them separately. |
| Movie/series matching | `movie_match_queue` / `series_root_match_queue`, pending file/root | Shared queued/running counts from claim tokens, plus matching task duration/progress. Retry gates and terminal match policy stay in metadata repositories. Empty candidates are a domain result, not necessarily a failed scheduled task. |
| Metadata refresh | Admin job, scheduled task, or library ingest | Admin/task attempt metrics and metadata dependency spans. Admin terminal state is read after durable writes. |
| Images | `metadata_image_cache_jobs`, one cache job; cache/backfill/repair scheduled tasks | Shared depth/age plus task attempts and S3 calls. Failed delivery/lease recovery retain existing retry policy. |
| Search index | `catalog_search_index_events`, one unprocessed event | Shared pending depth/oldest event and scheduled sync/rebuild attempts. Includes delayed retry events. |
| Chapters | Chapter-backfill scheduled task; remote extraction call | Task duration/outcome and authenticated node dependency span. Selected chapters and media paths are excluded from labels. |
| Intro/credits | Detection/contribution scheduled task | Task attempts/progress; FFmpeg process completion accounting for transcode/probe owners. Queue eligibility remains marker service state. |
| Subtitle AI | `subtitle_ai_jobs`, one translation/ASR job | Shared pending/running depth, semaphore wait and execution attempt. Reads committed terminal state after execution. Partial publication/cancellation uncertainty stays unknown. |
| Metadata AI | `metadata_translation_jobs`, one translation job | Same shared AI runner; separate queue. Global AI reaper reports recovered stale jobs. |
| Managed downloads | `download_artifacts`, one leased encode attempt | Shared queued/running depth, durable readiness/failure metrics, stale lease recoveries. Ready artifacts and linked download reconciliation are separate states. |
| History import | `history_import_runs`, one claimed import | Shared depth/age, execution/progress, claim-fenced success/failure/cancellation. Lost claim or failed terminal write stays unknown. |
| Recommendations | Scheduled refresh/embedding task; profile refresh operation | Task attempts, returned-error embedding/profile operations, dependency spans. A batch's success cannot establish that every item succeeded. |
| Notification fanout | `release_events`, one claimed batch | Depth/oldest event plus execution duration; only committed fanout is success. Realtime delivery failure does not undo durable inbox rows. |
| Server notification channels | Per-channel watermark, one claimed sweep | Actual nonempty sweeps report attempts. Sender/transaction failure is an error; empty polling is excluded. |
| Webhook/web/native push | `webhook_delivery_attempts`, `web_push_delivery_attempts`, `push_delivery_attempts` | Shared pending/retry depth and request age. Counts are outbox attempts, including backoff; terminal rows are excluded. Bounded notification dependency operations report send latency/outcome; use sender logs to inspect individual delivery state. |
| Email/digests | Notification inbox preference/cursor and verification outbox | Durable sender state plus bounded email send dependency latency/outcome. Mail-provider delivery after acceptance requires provider evidence. |
| Watch-provider sync | Scheduled sync/reconciliation task plus provider state | Task outcomes/progress and plugin host RPC spans. Provider histories and retries remain authoritative for individual records. |
| Plugin tasks | Task manager + plugin SDK RPC | Bounded `plugin` task attempts; host RPC names come from the pinned SDK. Plugin internal heaps/spans require plugin/SDK support. |
| Literary enrichment | Ebook/audiobook/manga/podcast scheduled tasks and domain eligibility/claim state | Metadata task attempts/progress and dependencies. Domain eligibility is not a universal queue table and is not counted as one. |
| Maintenance | Retention, repairs, collection sync and other fixed scheduled tasks | Shared `maintenance` category; no unbounded task-name dimension. Administrative task history supplies item-level evidence. |
| Playback/streaming | Playback lifecycle, streamtelemetry and direct-play owners | Existing byte/range/outcome metrics and node dependency timing. Child completions add CPU/peak RSS. Server timing cannot establish displayed frames or client rebuffering. |

The seven `silo_work_*` families cover active attempts, completions, duration,
queue wait, progress count/timestamp and recoveries. Histograms use fixed buckets
through one hour for execution and one day for waiting. Task-managed orchestration
and durable execution can share a category; an alert should specify which queue
or execution boundary it diagnoses, and never infer item throughput from attempts.

## Shared queue sampling

API processes sample the fixed queries in `internal/workmetrics/queues.go` every
30 seconds. Each query has a two-second deadline including pool wait. No query
runs in a scrape. Query errors immediately suppress depth/age values and increment
`sample_errors_total`; samples older than 90 seconds also suppress those values.
Available empty queues export zero depth and no oldest timestamp.

Each API replica observes the same database. Attach a `cluster` scrape label and
use `max by (cluster, queue, state)` for depth and `min` for the oldest request timestamp. Use `sum` only for per-process
attempt, error, duration and byte counters. A stale or missing node's metrics are
not equivalent to idle work; retain `up`, availability and sample-age panels.
Synchronize fleet clocks for comparing worker `sampled_at` with API health times.

## Cardinality and cost

Workload labels are limited by the switch in `workmetrics.Category`; outcomes
have five values. Queue/state pairs are fixed in source. There is no job, account,
profile, media, path, provider slug or error-message dimension. Histograms create
one series per bucket plus count/sum, so include those in capacity estimates.

Start a new deployment with a 20,000-series scrape limit and a 10-second timeout,
then compare the actual scrape before changing that cap. That limit is an operator
starting point, not a guarantee of all possible existing Silo series. Alert well
before hitting it: a rejected scrape loses the whole target. Retention and total
memory depend on scrape frequency, series churn, replica count and Prometheus
storage settings; measure the fleet instead of multiplying per-node host/GPU data.

Normal instrumentation should stay within 2% CPU/throughput and 5% p95 latency
of a matched build after measurement noise. These are rollout targets, not claims
that every workload has been benchmarked. Profiling has a separate measured cost;
keep CPU, trace and contention investigations separate. Monitor scrape p99 against
one second and record scrape bytes/sample counts alongside request performance.
