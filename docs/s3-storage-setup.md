# S3 Storage Setup

Silo uses S3-compatible object storage for caching artwork, catalog exports, and other operational data. Any S3-compatible backend works (AWS S3, Ceph RGW, MinIO, Cloudflare R2, etc.).

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
