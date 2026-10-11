# AI provider authentication

The shared AI client serves subtitle translation, metadata translation, and
Whisper-compatible transcription. Text requests snapshot `ai.auth_mode`, model,
and reasoning effort at request start. API-key mode uses the configured
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

The browser holds the pasted URL and authorization link in memory, clears the
dialog's state when it closes or sign-in ends, and discards unobserved login
mutations without a cache retention period. It does not persist them to browser
storage. Connection polling uses the attempt identifier to ignore another
attempt's outcome. Silo parses the address without requesting it. It requires
the exact prepared scheme, host, port, and path, and rejects userinfo, fragments,
duplicate query parameters, and oversized fields. A malformed or mismatched
paste does not consume the pending attempt.

Silo generates PKCE S256, state, and OIDC nonce for each attempt. Initial
registration uses `dynamic_agent_client`, `agent_name_hint=Silo`, and a stable
installation host ID. Later attempts reuse the saved issued client ID. A
returning callback cannot change the registration's verified subject. A retained
validated ID token supplies `id_token_hint` on reconnect, paired with that
registration's saved email. Disconnect clears the token hint. Authorization
URLs carrying it must not be logged.

Silo durably consumes a valid pending attempt before exchanging the
authorization code. A replay, node failure, or lost reply cannot exchange it
again. A newer sign-in, account selection, or disconnect supersedes an older
completion even while it waits for the provider. Silo validates the ID token's
RS256 signature, OpenAI issuer, audience, expiration, nonce, and subject, and
requires the granted `chatgpt.tokens.use.direct` scope before activating a grant.

The complete bundle lives under `ai.chatgpt.credentials` in the encrypted
settings store, including host ID, per-account registration, identity, tokens,
and pending state. Generic admin settings cannot read or overwrite this key,
and refreshes do not change the admin settings revision. Encryption remains
bound to the setting's key name.

Every node reads the active credentials on demand. Refresh occurs inside the
settings repository's transaction with a dedicated lock for this machine-managed
key, so nodes cannot concurrently reuse a rotating refresh token. This key is
excluded from the general settings surface and its validation lock. Waiting on
OpenAI does not block unrelated settings. Access token, refresh token, scopes,
and expiry are committed together. A refresh without a replacement refresh token
retains the existing token. Accepted refreshes and login exchanges finish under an
independent, bounded context so request or job cancellation cannot discard a
replacement. The final database write has its own bounded context. The verified
sign-in identity remains authoritative during refresh; optional refresh ID
tokens are not used. A rejected refresh marks the account as needing login.
Transient provider failures leave it retryable. Sign-in expiry is checked before
consuming the attempt; an accepted exchange may finish after that deadline.
A provider-side rotation followed by a lost database commit may require login
again; Silo never promises an atomic transaction across OpenAI and PostgreSQL.

Disconnect clears tokens and retains registration and host identity for later
authorization. The response distinguishes confirmed provider revocation from
local credential removal. Credential requests reject redirects, and OIDC
discovery URLs must remain on `https://auth.openai.com`.

## Inference

Subscription credentials go only to `https://api.openai.com/v1/responses` and
the account model catalog. The client sends an input array, maps system messages
to developer messages, and sets `store:false` and `stream:true`. It omits
temperature. Explicit effort uses `reasoning.effort`; API-key Chat Completions
requests use `reasoning_effort`.

The stream reader buffers output until `response.completed`. Failed, incomplete,
or truncated streams cannot publish partial translations. Account usage-limit
errors terminate retries; temporary usage-check failures use bounded backoff
and retain credentials. An inference or model-catalog HTTP 401 invalidates only
the rejected access token, then retries once with serialized token refresh. A
second rejection marks that account as needing login. Comparing the rejected
token protects credentials replaced concurrently by another node.
The account model catalog preserves provider order and includes only entries
with `visibility:list`.

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
