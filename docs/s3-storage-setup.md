# S3 Storage Setup

S3 is optional for artwork. A single-node install uses local artwork storage by default; S3 is recommended for multi-node deployments and remains available for catalog exports and other operational data. Any S3-compatible backend works (AWS S3, Ceph RGW, MinIO, Garage, Cloudflare R2, etc.).

## Core Settings

Configure these in **Admin > Settings > Storage** or via `server_settings`:

| Setting | Description |
|---------|-------------|
| **Endpoint** | S3 API endpoint (e.g. `https://s3.amazonaws.com`, `https://<id>.r2.cloudflarestorage.com`) |
| **Region** | AWS region (defaults to `us-east-1`) |
| **Bucket** | Bucket name |
| **Path Style** | Use path-style URLs (enable for most non-AWS backends) |
| **Access Key** | S3 access key ID |
| **Secret Key** | S3 secret access key |

## URL Auth Methods

Controls how Silo generates read URLs for cached images served to clients. Three modes are available:

### S3 Presigned URLs (default)

Standard S3 presigned URLs using the configured endpoint. Works with any S3-compatible backend out of the box — no additional setup required.

Images are served directly from the S3 endpoint with time-limited signed URLs.

### Public (no auth)

Serves images via an unsigned public URL through a custom domain. Use this when your bucket is publicly readable (e.g. Cloudflare R2 with a public custom domain) and you don't need URL-level access control.

**Additional setting:**
- **Read Endpoint** — The public CDN domain bound to the bucket (e.g. `https://cdn.example.com`)

### Cloudflare Token Auth

Generates HMAC-signed URLs validated by a Cloudflare WAF rule. Best for Cloudflare R2 with a custom domain when you want URL-level access control without exposing the R2 API endpoint.

**Additional settings:**
- **Read Endpoint** — R2 custom domain (e.g. `https://cdn.example.com`)
- **Token Secret** — HMAC-SHA256 shared secret (must match the WAF rule)
- **Token Param** — Query parameter name (default: `verify`)
- **Token TTL** — Token lifetime in seconds (default: `10800` = 3 hours)

Silo reuses the token for revisioned artwork, such as `w500.<revision>.webp`, for a quarter of the Token TTL, capped at a day, so clients and the CDN keep their cached images. Each URL stays valid for at least three quarters of the TTL. A longer TTL means fewer URL changes and a longer life for a leaked URL.

---

## Cloudflare R2 Setup Guide

### Option A: Public bucket (simplest)

1. **Create an R2 bucket** in the Cloudflare dashboard
2. **Connect a custom domain** under R2 > your bucket > Settings > Custom Domains
3. **Create an R2 API token** with read/write permissions for the bucket
4. **Configure Silo:**

| Setting | Value |
|---------|-------|
| Endpoint | `https://<account_id>.r2.cloudflarestorage.com` |
| Bucket | Your bucket name |
| Access Key | R2 API token access key |
| Secret Key | R2 API token secret key |
| Path Style | Enabled |
| URL Auth Method | Public (no auth) |
| Read Endpoint | `https://your-custom-domain.com` |

### Option B: Token-authenticated (recommended)

Adds HMAC-based access control so only Silo can generate valid image URLs.

**Requires Cloudflare Pro plan or higher** for the `is_timed_hmac_valid_v0()` WAF function.

#### Step 1: Generate a shared secret

```bash
openssl rand -hex 32
```

Save the output for Steps 2 and 3.

#### Step 2: Create a Cloudflare WAF rule

1. **Cloudflare Dashboard** > your zone > **Security** > **WAF** > **Custom rules**
2. Click **Create rule**
3. Name: `Silo CDN Token Auth`
4. Switch to **Edit expression** and enter:

```
(http.host eq "your-cdn-domain.com" and not is_timed_hmac_valid_v0("YOUR_SECRET", http.request.uri, 10800, http.request.timestamp.sec, 8))
```

Replace:
- `your-cdn-domain.com` with your R2 custom domain
- `YOUR_SECRET` with the secret from Step 1
- `10800` with your desired TTL in seconds (must match Silo's Token TTL)
- `8` with the separator length (`len(token_param) + 2`, e.g. `?verify=` = 8)

5. Set action to **Block**
6. **Deploy**

#### Step 3: Configure Silo

| Setting | Value |
|---------|-------|
| Endpoint | `https://<account_id>.r2.cloudflarestorage.com` |
| Bucket | Your bucket name |
| Access Key | R2 API token access key |
| Secret Key | R2 API token secret key |
| Path Style | Enabled |
| URL Auth Method | Cloudflare Token Auth |
| Read Endpoint | `https://your-cdn-domain.com` |
| Token Secret | Same secret from Step 1 |
| Token Param | `verify` (default) |
| Token TTL | `10800` (default, must match WAF rule) |

#### Step 4: Verify

Restart the server, then check that image URLs in API responses look like:

```
https://your-cdn-domain.com/tmdb/movies/550/poster/original.jpg?verify=1712150400-abc123...
```

**Troubleshooting:** If images don't load, temporarily change the WAF rule action from Block to Log to debug without breaking access.

---

## Traditional S3 Setup (AWS, Ceph, MinIO)

1. Create a bucket with appropriate IAM permissions (`PutObject`, `GetObject`, `DeleteObject`)
2. Configure Silo with the endpoint, bucket, and credentials
3. Leave URL Auth Method as **S3 Presigned URLs** (default)

No additional setup needed — presigned URLs work out of the box.

---

## Garage Setup Guide

[Garage](https://garagehq.deuxfleurs.fr/) is a lightweight, actively maintained
S3-compatible object store aimed at self-hosters. It implements the core S3 API
(`PutObject`, `GetObject`, `DeleteObject`, multipart upload, bucket CORS) that
Silo needs, so it works as a drop-in backend the same way MinIO or Ceph RGW do
— with one difference worth knowing up front: **Garage has no S3 bucket-policy
or object-ACL support** (`PutBucketPolicy`, `PutObjectAcl`, and friends are all
unimplemented — see [Garage's S3 compatibility
matrix](https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-compatibility/)).
Public, unsigned reads are handled entirely differently: through Garage's own
[website-access
feature](https://garagehq.deuxfleurs.fr/documentation/cookbook/exposing-websites/),
which binds a bucket to a domain either by exact bucket-name match or as a
subdomain of a configured `root_domain` — not by a bucket policy on the normal
S3 API endpoint.

### Option A: Presigned URLs (simplest, no extra setup)

Garage's S3 API supports standard SigV4 signing, so the default **S3 Presigned
URLs** mode works with no additional Garage-specific Silo configuration:

1. Create a bucket and an access key scoped to it:
   ```
   garage bucket create <bucket-name>
   garage key create <key-name>
   garage bucket allow --key <key-name-or-ID> --read --write <bucket-name>
   ```
2. Configure Silo with the endpoint, bucket, and credentials, **Path Style**
   enabled
3. Set Silo's **Region** to match Garage's configured `[s3_api].s3_region` in
   `garage.toml` — Garage validates the SigV4 signing scope against this
   value, so a mismatch breaks presigned URLs even with a correct endpoint
   and credentials. `s3_region` has no built-in default (Garage requires it
   set explicitly); Garage's own quick-start guide uses `garage`, not AWS's
   `us-east-1`, so this almost always needs to be changed from Silo's own
   default.
4. Leave URL Auth Method as **S3 Presigned URLs** (default)

This is the recommended starting point and is sufficient for most
self-hosted deployments.

### Option B: Public (no auth), for a public asset domain

Public mode is usable with Garage, but — because Garage has no bucket-policy
concept — it requires the bucket itself to be named after the domain you want
to serve it from, and Garage's separate website listener (not the main S3 API
port) to be what Silo's Read Endpoint actually reaches:

1. **Name the bucket as the exact public domain**, e.g. `garage bucket create
   assets.example.com` (Garage matches the website Host header against the
   bucket name itself, or against `<bucket>.<root_domain>` if you'd rather use
   a shared suffix — see the website-access cookbook linked above)
2. **Enable website access on that bucket**: `garage bucket website --allow
   assets.example.com`
3. **Configure and start Garage's website listener** — it does not listen by
   default, this section has to be added to `garage.toml`. `root_domain` is a
   required field of Garage's `[s3_web]` config in v2.4.1 (the config fails
   to load without it, even though it only affects `<bucket>.<root_domain>`
   -style routing) — any placeholder value works if you only need exact
   bucket-name matching:
   ```toml
   [s3_web]
   bind_addr = "[::]:3902"
   root_domain = ".web.example.com"
   ```
4. **Route the domain to that port** — e.g. a Caddy/nginx vhost for
   `assets.example.com` proxying to `garage:3902` with the Host header
   preserved
5. **Configure Silo:**

| Setting | Value |
|---------|-------|
| Endpoint | Garage's S3 API endpoint, e.g. `https://garage.internal:3900` |
| Bucket | `assets.example.com` |
| Region | must match `garage.toml`'s `[s3_api].s3_region` — see Option A |
| Path Style | Enabled |
| URL Auth Method | Public (no auth) |
| Read Endpoint | `https://assets.example.com` |

Garage's `[s3_api].api_bind_addr` listener has no built-in TLS support either
(same as the website listener) — an `https://` endpoint only works if a
reverse proxy in front of Garage terminates TLS and forwards to that port.
Point Silo's Endpoint at that proxy, not directly at `api_bind_addr`, if you
want HTTPS to the S3 API.

To verify the route, request a known object path rather than the domain root.
Website mode serves objects, not a bucket index, so a bare `GET
https://assets.example.com/` answers `404` on a correctly configured bucket
unless you have set an index document. That `404` is expected and is not a
sign of a broken setup.

Private/operational buckets (metadata, user DB) are unaffected by this — keep
those on the default presigned mode against the normal S3 API port; only the
bucket you want served as public, unsigned reads needs the website-domain
treatment above.

**Not supported today:** the `MakeObjectPublic` per-object ACL path in
`internal/s3client` (`PutObjectAcl` with a `public-read` canned ACL) will fail
against Garage, since object ACLs are unimplemented. It is not currently
called anywhere in this codebase, but a future caller should not assume it
works on every configured backend.
