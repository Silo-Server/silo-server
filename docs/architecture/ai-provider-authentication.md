# AI provider authentication

The shared AI client serves subtitle translation, metadata translation, and
Whisper-compatible transcription. Text requests snapshot `ai.auth_mode`, endpoint,
API key, model, reasoning effort, and active registration at request start.
API-key mode uses the configured
OpenAI-compatible endpoint. ChatGPT mode uses the documented OSS token-sharing
flow and the fixed OpenAI Responses endpoint. Transcription uses the existing
speech endpoint and API key regardless of text authentication mode.

## Identity and credentials

The administrator starts a ten-minute sign-in attempt from AI Services settings.
Silo returns an authorization link, a loopback callback URI, and an attempt
identifier. After OpenAI authorization, the administrator copies the final
callback URL from the browser address bar and submits it to Silo through an
acting-administrator operation. The loopback tab can show a connection error
because this manual flow runs no local listener. This works independently of
where the Silo server runs, including remote servers and containers.

The browser binds connection mutations and their results to the initiating
account, server, and profile authority. An authority change clears the dialog
and its mutation state; a late response cannot reopen it or send the previous
callback under a new session. Completion, selection, and disconnect refresh
status, model catalogs, and viewer readiness even when an error follows a
committed credential change. The browser holds the pasted URL and authorization
link in memory, clears the dialog's state when it closes or sign-in ends, and
discards unobserved login mutations without a cache retention period. It does
not persist them to browser storage. Connection polling uses the attempt identifier to ignore another
attempt's outcome. Silo parses the address without requesting it. It requires
the exact prepared scheme, host, port, and path, and rejects userinfo, fragments,
duplicate query parameters, and oversized fields. A malformed or mismatched
paste does not consume the pending attempt.

Silo generates PKCE S256, state, and OIDC nonce for each attempt. Initial
registration uses `dynamic_agent_client`, `agent_name_hint=Silo`, and a stable
installation host ID. Later attempts reuse the saved issued client ID. A
returning callback cannot change the registration's verified subject. Silo
rejects a verified callback for another subject and attempts bounded revocation
of its newly issued grant outside the settings transaction, preserving the saved
account. A retained validated ID token supplies `id_token_hint` on reconnect,
paired with that registration's saved email. Disconnect clears the token hint. Authorization
URLs carrying it must not be logged.

Silo durably consumes a valid pending attempt before exchanging the
authorization code. A replay, node failure, or lost reply cannot exchange it
again. A newer sign-in, account selection, or disconnect supersedes an older
completion even while it waits for the provider. Silo validates the ID token's
RS256 signature, OpenAI issuer, audience, expiration, nonce, and subject, and
requires the granted `chatgpt.tokens.use.direct` scope before activating a grant.
When the initial token response omits scopes, Silo uses the requested scopes
under RFC 6749 section 5.1. The resulting plan grant still requires a refresh token.

A verified sign-in without plan permission retains a disabled registration and
identity hints, including its accepted access and refresh tokens so Disconnect
can revoke the grant. The registration needs login and cannot serve inference,
model-catalog requests, or account selection. Reconnect reuses the issued client
ID and requests consent; ordinary reconnects do not force consent. A declined
re-consent attempt preserves an existing usable account.

Discovery and signing-key reads retry transient network failures and HTTP 429 or
5xx responses within the completion deadline. Retry delays that cannot fit the
remaining budget return immediately. Authorization-code exchanges are never
retried, and invalid metadata or identity proofs stop verification.

An initial authorization-code `invalid_grant`, or an accepted exchange whose
identity reads remain unavailable, retains only the issued client ID after
callback state validation. It stores no identity or inference credentials.
Retry reuses that ID and binds its first verified subject only after successful
identity validation. Invalid identity proofs and superseded attempts cannot
create registrations.

The complete bundle lives under `ai.chatgpt.credentials` in the encrypted
settings store, including host ID, per-account registration, identity, tokens,
and pending state. Generic admin settings cannot read or overwrite this key,
and refreshes do not change the admin settings revision. Encryption remains
bound to the setting's key name.

Account activation coordinates the encrypted credential value with model and
effort settings in one transaction. It takes the credential lock before the
general settings lock on the same connection, after fetching the target account's
catalog outside the transaction. In ChatGPT mode it retains a supported model or
chooses the first listed model and resets incompatible effort to the provider
default. Catalog failures prevent activation; accepted login credentials remain
available for a later selection. API-key settings are preserved.
Activation returns settings snapshots captured before and after its writes in
that transaction. The API exposes only signed validators. The web advances an
unrelated draft's baseline only when both validators and its authority match,
with no overlapping settings save or dirty model/effort field.

Model-setting writes fetch the catalog outside settings transactions and recheck
the active registration at commit. Each text request reads the complete provider
configuration and active registration from one database snapshot. Provider
dispatch uses that snapshot, avoiding stale authentication modes or models
during asynchronous configuration reload.
Draft connection checks retain the submitted model and effort.

Subtitle and metadata translation check the live text configuration before
inserting jobs. Disconnected accounts and registrations needing login reject
new work, including automatic and viewer-triggered translation. A job records
the resolved model in its provenance and deduplication key, then keeps the same
provider snapshot in memory for queued work, batches, and retries. Plain
transcription does not consult text connection state. Automatic model and
effort changes notify the local config watcher and other nodes after commit.

Standalone text requests and translation jobs select the active ChatGPT
registration once. Their retries and
token refreshes retain that registration even if an administrator switches the
active account. Disconnecting that registration stops recovery; retries never
fall back to another account. Model-catalog requests follow the same rule.
Every node reads that registration's credentials on demand. Refresh occurs inside
the settings repository's transaction with a dedicated lock for this
machine-managed key, so nodes cannot concurrently reuse a rotating refresh token. This key is
excluded from the general settings surface and its validation lock. Waiting on
OpenAI does not block unrelated settings. Local waiters share admission by pool
and key before acquiring connections; idle admission entries are removed.
Access token, refresh token, scopes,
and expiry are committed together. A refresh without a replacement refresh token
retains the existing token. Accepted refreshes and login exchanges finish under an
independent, bounded context so request or job cancellation cannot discard a
replacement. The final database write has its own bounded context.
Accepted login persistence gets an independent two-minute deadline so it can
wait for an admitted one-minute refresh and its final database commit before
acquiring the shared credential lock.
Token expiry is measured from exchange completion, before identity, catalog,
and persistence waits.
Returned scopes and rotated refresh tokens are retained when a successful refresh
drops plan access; inference stops and reconnect explicitly requests consent. The verified
sign-in identity remains authoritative during refresh; optional refresh ID
tokens are not used. Only the documented unusable-token errors (`invalid_grant`,
`invalid_refresh_token`, `token_expired`, `refresh_token_expired`,
`refresh_token_invalidated`, and `refresh_token_reused`) mark the account as
needing login. A delayed rejection of its old access token cannot clear that
requirement. Other, empty, and malformed OAuth errors leave it retryable.
Refresh makes up to three attempts for transport failures, HTTP 429 or 5xx, and
documented temporary OAuth failures inside the serialized rotation operation.
Backoff honors `Retry-After` within the independent deadline. Exhaustion preserves
the saved credentials; authorization-code exchanges remain single attempts.
Sign-in expiry is checked before
consuming the attempt; an accepted exchange may finish after that deadline.
A provider-side rotation followed by a lost database commit may require login
again; Silo never promises an atomic transaction across OpenAI and PostgreSQL.

Disconnect first commits a disabled account, clears its access token and active
selection, and retains its refresh token with an operation identifier. Fresh and
pinned token sources reject the disabled account. Provider calls run outside the
credential lock. Disconnect makes up to three revocation attempts, backing off
after network failures and HTTP 429 or 5xx responses before clearing the refresh token.
Cleanup checks the operation identifier, preserving a newer disconnect or
successful reconnect, another account selection, and newer pending login. A
failed cleanup commit leaves the account disabled with its retry token.
The provider deadline
leaves time for local cleanup, and caller cancellation does not interrupt it.
Registration and host identity remain available for later authorization.
The browser offers Disconnect for every saved registration, including inactive
accounts that cannot supply inference credentials.
The response distinguishes confirmed provider revocation from local credential
removal. Missing local tokens do not confirm remote revocation, including after
an earlier disconnect cleared them without provider confirmation.
Credential requests reject redirects, and OIDC
discovery URLs must remain on `https://auth.openai.com`.

First-connection welcome history belongs to the shared server configuration. A
retained registration does not establish that history: successful activation
commits `has_connected` with the selected account and compatible settings. The
activation transaction supplies `X-Silo-ChatGPT-First-Connection`; matching login
status retains `login_first_connection` so a lost completion reply can reconcile.
The browser confirms a successful completion from its own response even if
another browser replaces the latest login status. Response and polling share
confirmation for the same local attempt; cancellation, a new attempt, unmounting,
and authority changes discard late results. Later activation, refresh, and
disconnect preserve the history. Legacy bundles with a selected account infer prior activation.

## Inference

Subscription credentials go only to `https://api.openai.com/v1/responses` and
the account model catalog. The client sends an input array, maps system messages
to developer messages, and sets `store:false` and `stream:true`. It omits
temperature. Explicit effort uses `reasoning.effort`; API-key Chat Completions
requests use `reasoning_effort`.

The stream reader strips one leading UTF-8 BOM and accepts LF, CRLF, and CR
line endings across transport reads, and buffers output until `response.completed`. Failed, incomplete,
or truncated streams cannot publish partial translations. Account usage-limit
errors terminate retries; temporary usage-check failures, streamed
`subscription_sharing_user_unavailable`, and `server_error` failures use bounded
backoff and retain credentials. Rate-limit delays accept both seconds and HTTP
dates in `Retry-After`, subject to request cancellation and deadlines.
An inference or model-catalog HTTP 401, or a streamed
`subscription_sharing_invalid_user` failure, invalidates only
the rejected access token, then retries once with serialized token refresh. A
second rejection marks that account as needing login. Comparing the rejected
token protects credentials replaced concurrently by another node.
A `subscription_sharing_user_not_eligible` HTTP or stream failure persists a
separate plan-eligibility restriction for the request's registration and exact
access token. It stops retries and later token, catalog, selection, and translation
readiness reads. The account remains available for disconnect, and the web explains
the restriction without offering reconnect. A late response cannot disable a
replacement token or a different active account. API-key requests and speech
transcription do not use this restriction.
A `subscription_sharing_usage_limit_exceeded` HTTP or stream failure persists
`usage_limited` for the exact registration and access token. Fresh and pinned
inference sources and translation admission reject the pause. Silo assumes no
reset time. The administrator can deliberately recheck usage through Test text
model; only a completed successful check clears the matching token's pause.
Failed checks preserve it. Catalog reads, model-setting writes, selection,
refresh, and credential renewal do not clear it. Paused accounts remain
selectable for a check, and credentials needing login can be renewed first.
Eligibility and disconnect checks still apply to explicit checks. Ordinary
request success cannot resume a pause. Each provider limit rejection rotates an
internal pause identifier; a delayed successful check cannot clear a newer pause
on the same access token. Local pause admission uses a separate error from a
fresh provider limit, so blocked retries cannot replace the pause being checked.
API-key and speech requests do not read
or change this state.
The account model catalog preserves provider order and includes only entries
with `visibility:list`, excluding the non-streaming `o1-pro`, `o3-pro`, and
`gpt-5.5-pro` families. Catalog reads make up to three attempts for transient
transport/read failures and HTTP 429/5xx responses. Backoff honors `Retry-After`
within one minute and any earlier caller deadline, keeping the same account token.
Unauthorized responses
use the existing single refresh recovery; malformed or terminal responses do not retry.

OpenAI-compatible requests omit reasoning effort when empty, and omit
temperature for known reasoning models or an explicit effort. Known model
families reject unsupported effort before request execution. Custom providers
may implement a subset of the configured vocabulary; their connection check
establishes whether a particular model accepts it.

The administrator account applies to server text AI jobs. It does not implement
Silo user login or individual household billing. No native media, subtitle,
metadata, or Jellyfin response contract changes.

## Provider references

- [OSS sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)
- [Self-hosted deployments](https://developers.openai.com/siwc/token-sharing-open-source/self-hosted-vms)
- [Models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference)
- [Profiles and sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)
- [Errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)
- [Reasoning effort](https://developers.openai.com/api/docs/guides/reasoning)
