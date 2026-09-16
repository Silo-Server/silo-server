# Downloaded subtitle storage

Subtitle objects live in the assets blob store (`internal/blobstore`), so either
a local filesystem root or the public S3 bucket backs them; see
[blob-storage.md](blob-storage.md). Subtitles own the `subtitles/` key prefix,
which is what separates them from artwork when a local backend puts both in one
root. Bytes always flow through the server — a client never receives a signed
storage URL for a subtitle — so the backend is invisible across the API and to
compatibility clients. The `downloaded_subtitles.s3_key` column keeps its name
and holds a backend-neutral object key.

Downloaded subtitle content has separate logical and physical identities. The
logical identity is the media file, provider, language, format, and full SHA-256
of the bytes. PostgreSQL enforces uniqueness for rows with a known digest. Every
new publication uses a fresh UUID object key; keys never move when metadata
changes and are never reused by later publications of the same content.

Concurrent publishers may upload separate candidate objects. Only one row wins
the content identity constraint. A confirmed losing insert may delete its own
unpublished candidate. An uncertain insert error must retain its object: the
transaction may have committed before its reply was lost. Retrying identical
content can recover the committed row through the full content identity.

Legacy rows have no full digest. Their old object keys contain only 32 bits of a
hash, which cannot establish content identity. A candidate legacy duplicate is
read and its full content hash compared before reuse. Language edits obtain a
missing full digest from the stored bytes. Migration does not fabricate digests
or rewrite existing objects.

Metadata updates merge only supplied fields in one SQL update. An optional
expected revision is compared in that same statement. A database trigger
increments the revision for every update, including bridge and direct SQL
writers, so an intervening change invalidates a captured guard. Language changes
can conflict with an existing full content identity; a conflict leaves both rows
and objects intact.

Deletion removes the row before attempting object cleanup. A delayed cleanup
cannot remove a new publication because its object key differs. Successful
deletion means metadata is absent, not that physical cleanup is durable. Failed
cleanup and uncertain publication can leave orphan objects; they are logged,
but there is no durable orphan reconciliation job. A client must not infer a
physical erasure guarantee or durable operation replay from these methods.

All subtitle writers must use the immutable object implementation before relying
on these publication guarantees. The revision trigger invalidates guards for
older writers, but cannot make an older binary's object moves or shared-key
cleanup safe. This storage foundation alone does not enable API v2 mutations or
activate playback for existing accounts.

AI job cancellation attempts the guarded terminal job-state transition before
canceling a local worker context. A database error is returned even when local
work can be asked to stop. Already-terminal rows remain unchanged. A successful
cancel request can race with successful completion; callers must read the job
state to distinguish those outcomes.

AI output uploads to a private candidate object before opening a publication
transaction. The transaction locks the active job row, checks the exact media
file and requesting account, and publishes or reuses subtitle metadata. Final
output and job completion commit together. Cancellation and stale-job recovery
compete for that same row lock. If a terminal transition wins, the candidate
cannot become published metadata. If final publication wins, a later guarded
cancel leaves the completed job unchanged. Intermediate transcripts also pass
the active-job fence; transcripts committed before later cancellation remain
available. Reused tracks are locked against deletion or metadata edits until
the publication transaction completes. Legacy reuse requires full byte-hash
verification and a transactional recheck of the immutable key and identity.

Confirmed refusal or duplicate reuse cleans only the unpublished candidate.
An uncertain transaction reply retains the candidate because publication may
have committed. Ready/completed notifications require a confirmed publication
result; a refused stale publisher does not announce another terminal outcome.
An unknown commit outcome remains distinct through the service boundary: it
causes neither a failure-state write nor a definitive failure notification.
Callers reconcile the persisted job instead of interpreting a lost reply as a
failed job.
Notifications remain best effort. There is no durable request replay receipt
or orphan cleanup guarantee.

Subtitle heartbeats signal confirmed terminal or missing rows to the shared
runner without updating terminal timestamps. The runner stops local contexts
only for that explicit signal; transient database failures retain previous
behavior. The existing 30-second heartbeat interval, provider cooperation, and
already-streamed transient cues limit how quickly work stops. The database
publication fence does not depend on prompt provider cancellation and does not
undo provider compute or previously committed output. All AI workers must use
this fenced publication path before relying on these guarantees; an older
worker can still bypass it. This change does not activate playback or port the
AI creation/cancellation wire operations.
