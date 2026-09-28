# Recommendation administration API

Recommendation administration uses acting-administrator authorization. All five
routes remain registered when the recommendation worker is absent and return
`503` until it is available. The frozen v1 handlers remain unchanged.

| Endpoint | Result |
| --- | --- |
| `GET /api/v2/admin/recommendations/status` | Counts and running flags for four job kinds |
| `POST /api/v2/admin/recommendations/trigger/embeddings` | `200` with `status: "started"` |
| `POST /api/v2/admin/recommendations/trigger/taste-profiles` | `200` with `status: "started"` |
| `POST /api/v2/admin/recommendations/trigger/cowatch` | `200` with `status: "started"` |
| `POST /api/v2/admin/recommendations/trigger/recommendations` | `200` with `status: "started"` |

Status contains `embeddings`, `taste_profiles`, `cowatch`, and `recommendations`.
Each has a `running` boolean and integer `count`; embeddings includes `total`
when nonzero. Counts come from persisted catalog/recommendation data. Running
flags describe the responding process and are sampled separately from counts.
They do not form a consistent cluster-wide snapshot.

Trigger success means the existing worker claimed its process-local running flag
and launched background work. It does not mean that work completed or that a
job was persisted. There is no job ID, Location header, durable acceptance,
cluster-wide exclusion, or automatic recovery promise. A duplicate trigger while
the same kind is running in that process returns `409`. Another process may
start the same kind independently; a restart loses the local running flag.

All four triggers are non-retryable. The web disables both TanStack mutation
retries and authentication refresh/replay for these actions, including after
`401`. After an uncertain result, inspect status before deciding whether to
trigger again. The status query continues to poll every five seconds.

These existing administration flows have no Apple or Android callers and no
Jellyfin compatibility equivalent. Viewer recommendation endpoints are separate.
