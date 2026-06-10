# Core Push — Web Client & Admin Config — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make push operable end-to-end from the browser: a Web Push client (service worker, subscribe, receive, per-device control, device list) plus an admin provider-config page (APNs/FCM/WebPush credentials, VAPID keygen, readiness), on top of the frozen push API.

**Architecture:** Five web units + two small Go endpoints. A static service worker receives pushes; a `usePushDevice` hook owns the browser subscription lifecycle; the existing notifications-settings page gains a push control + device list; a new admin page reuses the existing encrypted-settings write path for credentials. Two net-new server endpoints: VAPID keygen and a self-targeted test push.

**Tech Stack:** React 19, TanStack Query, sonner, the existing api client (auto device/profile headers), browser Push API / Service Worker / Notifications API, Go (chi), webpush-go (already a dep). Vitest + RTL.

**Spec:** `docs/superpowers/specs/2026-06-10-core-push-web-client-design.md`. The push registration API + delivery worker are frozen (Phase-3-server).

Commands assume the repository root is the cwd; web commands run in `web/` with `pnpm`. Go: `export GOWORK=off`.

## Frozen API the client calls

```
GET    /api/v1/notifications/push/webpush-key          → {vapid_public_key}
PUT    /api/v1/notifications/push/device  {transport, token}   → 204  (needs X-Profile-Id; api client sends it)
DELETE /api/v1/notifications/push/device                → 204
GET    /api/v1/notifications/push/devices               → {devices:[{device_id,name,platform,transport,push_enabled,registered_at}]}
PUT    /api/v1/notifications/push/devices/{device_id} {enabled}  → 204
GET    /api/v1/admin/push/status                        → {apns,fcm,webpush: bool}
PUT    /api/v1/admin/settings/{key}  {value}            → writes an encrypted push.* key (existing)
GET    /api/v1/admin/settings/sensitive-status          → {configured:[keys]}  (existing)
```

Net-new this plan: `POST /api/v1/admin/push/generate-vapid-keys` and `POST /api/v1/admin/push/test`.

## File map

| File | Action | Responsibility |
|---|---|---|
| `internal/api/handlers/push.go` | modify | add `HandleGenerateVAPIDKeys`, `HandleSendTestPush`; add a `notifier` dep |
| `internal/api/router.go` | modify | register the two admin routes |
| `cmd/silo/main.go` | modify | pass the notifications service into the push handler |
| `web/public/sw.js` | create | service worker: push, notificationclick, pushsubscriptionchange |
| `web/src/lib/pushSw.ts` | create | pure SW logic (notification options, click URL) — tested; mirrored in sw.js |
| `web/src/lib/push.ts` | create | `urlBase64ToUint8Array`, VAPID-key cache helpers |
| `web/src/main.tsx` | modify | register `/sw.js` when supported |
| `web/src/api/types.ts` | modify | `PushDeviceInfo`, `VapidKeyPair` |
| `web/src/hooks/queries/push.ts` | create | query/mutation hooks (webpush-key, devices, toggle, register/revoke, admin keygen/test) |
| `web/src/hooks/usePushDevice.ts` | create | browser subscription lifecycle state machine |
| `web/src/hooks/usePushDevice.test.tsx` | create | hook tests |
| `web/src/pages/settings/NotificationsSettings.tsx` | modify | push control + device list |
| `web/src/pages/admin-settings/PushSettings.tsx` | create | three credential cards + keygen + test |
| `web/src/pages/admin-settings/AdminSettingsLayout.tsx` | modify | nav entry |

---

### Task 1: Server endpoints — VAPID keygen + test push

**Files:**
- Modify: `internal/api/handlers/push.go`, `internal/api/router.go`, `cmd/silo/main.go`
- Test: `internal/api/handlers/push_test.go`

- [ ] **Step 1: Add a notifier dep + the two handlers**

In `push.go`, add a narrow interface and a field on `PushHandler`:

```go
// pushNotifier creates a notification for the calling user (test push).
type pushNotifier interface {
	CreateSystem(ctx context.Context, userID int, typ, title, body string)
}
```

Add `notifier pushNotifier` to `PushHandler` and a parameter to `NewPushHandler` (nil-tolerant — test-push 503s if absent). `*notifications.Service` satisfies `pushNotifier` (it has `CreateSystem(ctx, userID int, typ, title, body string)` from Phase-1).

Handlers:

```go
func (h *PushHandler) HandleGenerateVAPIDKeys(w http.ResponseWriter, r *http.Request) {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"vapid_public": pub, "vapid_private": priv})
}

func (h *PushHandler) HandleSendTestPush(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "notifications unavailable")
		return
	}
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "auth required")
		return
	}
	h.notifier.CreateSystem(r.Context(), userID,
		"system.test_push", "Test push",
		"If you can see this on a device, push delivery is working.")
	w.WriteHeader(http.StatusAccepted)
}
```

Import `webpush "github.com/SherClockHolmes/webpush-go"`. **Verify the `GenerateVAPIDKeys()` return order** against the installed version — webpush-go returns `(privateKey, publicKey string, err error)`. Confirm by reading the vendored signature or `go doc`; if it's `(public, private, err)` swap accordingly. Get this right — swapping the keys silently breaks push.

- [ ] **Step 2: Routes**

In `router.go`, in the admin push block (next to `r.Get("/push/status", ...)`, ~line 2295, behind `deps.PushStore != nil`):

```go
r.Post("/push/generate-vapid-keys", pushHandler.HandleGenerateVAPIDKeys)
r.Post("/push/test", pushHandler.HandleSendTestPush)
```

- [ ] **Step 3: Wire the notifier**

Update every `NewPushHandler(...)` call site (find with grep) to pass the notifications service. In `cmd/silo/main.go` and/or `router.go` where the push handler is constructed, add `deps.NotificationsService` (or the in-scope `notificationsSvc`) as the third arg. Build will tell you the sites.

- [ ] **Step 4: Tests**

Add to `push_test.go` (reuse the package's fakes / chi-request helper from Task-13 push tests):

```go
func TestPushHandleGenerateVAPIDKeys_ReturnsPair(t *testing.T) {
	h := NewPushHandler(&fakePushRegistry{}, &fakePushConfig{}, nil)
	rr := httptest.NewRecorder()
	h.HandleGenerateVAPIDKeys(rr, httptest.NewRequest(http.MethodPost, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d", rr.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["vapid_public"] == "" || body["vapid_private"] == "" || body["vapid_public"] == body["vapid_private"] {
		t.Fatalf("bad key pair: %+v", body)
	}
}

type fakeNotifier struct{ called bool; userID int; typ string }

func (f *fakeNotifier) CreateSystem(_ context.Context, userID int, typ, _, _ string) {
	f.called = true
	f.userID = userID
	f.typ = typ
}

func TestPushHandleSendTestPush_CreatesForCaller(t *testing.T) {
	n := &fakeNotifier{}
	h := NewPushHandler(&fakePushRegistry{}, &fakePushConfig{}, n)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r = r.WithContext(apimw.SetUserID(r.Context(), 7)) // use the package's claims-injection helper if SetUserID differs
	rr := httptest.NewRecorder()
	h.HandleSendTestPush(rr, r)
	if rr.Code != http.StatusAccepted || !n.called || n.userID != 7 || n.typ != "system.test_push" {
		t.Fatalf("unexpected: code=%d notifier=%+v", rr.Code, n)
	}
}

func TestPushHandleSendTestPush_NilNotifier503(t *testing.T) {
	h := NewPushHandler(&fakePushRegistry{}, &fakePushConfig{}, nil)
	rr := httptest.NewRecorder()
	h.HandleSendTestPush(rr, httptest.NewRequest(http.MethodPost, "/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d", rr.Code)
	}
}
```

Check how the existing push tests inject the user id into context (Task-13 used a claims/`apimw` helper — mirror it; `apimw.SetUserID` may not exist, use whatever the package's tests already use to set `GetUserID`).

- [ ] **Step 5: Build, test, commit**

```bash
GOWORK=off go build ./... && GOWORK=off go test ./internal/api/handlers/ -run TestPush -v 2>&1 | tail -8 && GOWORK=off go vet ./internal/api/handlers/
git add internal/api/handlers/push.go internal/api/handlers/push_test.go internal/api/router.go cmd/silo/main.go
git commit -m "feat(push): admin VAPID keygen and self-test endpoints"
```

---

### Task 2: Service worker + registration + helpers

**Files:**
- Create: `web/public/sw.js`, `web/src/lib/pushSw.ts`, `web/src/lib/push.ts`
- Test: `web/src/lib/pushSw.test.ts`, `web/src/lib/push.test.ts`
- Modify: `web/src/main.tsx`

- [ ] **Step 1: Pure SW logic (tested) — `web/src/lib/pushSw.ts`**

```ts
export interface PushPayload {
  id?: number;
  title?: string;
  body?: string;
  link?: string;
  category?: string;
}

export interface SwNotification {
  title: string;
  options: { body: string; data: { link: string }; tag: string };
}

/** Maps a raw push payload to showNotification args; tolerant of partial/missing fields. */
export function buildNotification(payload: PushPayload | null): SwNotification {
  const p = payload ?? {};
  const link = p.link && p.link.length > 0 ? p.link : "/notifications";
  const tag = p.id != null ? `n${p.id}` : "n";
  return {
    title: p.title && p.title.length > 0 ? p.title : "Silo",
    options: { body: p.body ?? "", data: { link }, tag },
  };
}

/** The URL a notification click should open/focus. */
export function clickUrl(data: unknown): string {
  const d = (data ?? {}) as { link?: string };
  return d.link && d.link.length > 0 ? d.link : "/notifications";
}
```

- [ ] **Step 2: Test it — `web/src/lib/pushSw.test.ts`**

```ts
import { describe, expect, it } from "vitest";
import { buildNotification, clickUrl } from "./pushSw";

describe("buildNotification", () => {
  it("maps a full payload", () => {
    const n = buildNotification({ id: 5, title: "Approved", body: "Dune", link: "/requests" });
    expect(n.title).toBe("Approved");
    expect(n.options).toEqual({ body: "Dune", data: { link: "/requests" }, tag: "n5" });
  });
  it("falls back on missing fields", () => {
    const n = buildNotification(null);
    expect(n.title).toBe("Silo");
    expect(n.options.body).toBe("");
    expect(n.options.data.link).toBe("/notifications");
    expect(n.options.tag).toBe("n");
  });
});

describe("clickUrl", () => {
  it("uses link when present", () => expect(clickUrl({ link: "/items/9" })).toBe("/items/9"));
  it("falls back to inbox", () => expect(clickUrl({})).toBe("/notifications"));
});
```

Run: `pnpm exec vitest run src/lib/pushSw.test.ts` → green.

- [ ] **Step 3: The static service worker — `web/public/sw.js`**

Self-contained (served statically, not bundled). Mirror the pure logic exactly (the canonical, tested source is `pushSw.ts`):

```js
// web/public/sw.js — static service worker for Web Push.
// NOTE: the buildNotification/clickUrl logic mirrors src/lib/pushSw.ts (the
// tested source of truth). Keep them in sync.

function buildNotification(payload) {
  const p = payload || {};
  const link = p.link && p.link.length > 0 ? p.link : "/notifications";
  const tag = p.id != null ? "n" + p.id : "n";
  return {
    title: p.title && p.title.length > 0 ? p.title : "Silo",
    options: { body: p.body || "", data: { link: link }, tag: tag },
  };
}
function clickUrl(data) {
  const d = data || {};
  return d.link && d.link.length > 0 ? d.link : "/notifications";
}

self.addEventListener("push", (event) => {
  let payload = null;
  try {
    payload = event.data ? event.data.json() : null;
  } catch (e) {
    payload = null;
  }
  const n = buildNotification(payload);
  event.waitUntil(self.registration.showNotification(n.title, n.options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = clickUrl(event.notification.data);
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      for (const c of wins) {
        if ("focus" in c) {
          c.postMessage({ type: "notification-click", link: url });
          return c.focus();
        }
      }
      return self.clients.openWindow(url);
    }),
  );
});

self.addEventListener("pushsubscriptionchange", (event) => {
  // Best-effort re-subscribe using the cached VAPID key; the app re-registers
  // the new subscription on next load if this fails.
  event.waitUntil(
    (async () => {
      try {
        const cache = await caches.open("silo-push");
        const res = await cache.match("vapid-public-key");
        if (!res) return;
        const vapid = await res.text();
        const sub = await self.registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: vapid,
        });
        await fetch("/api/v1/notifications/push/device", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          credentials: "include",
          body: JSON.stringify({ transport: "webpush", token: JSON.stringify(sub.toJSON()) }),
        });
      } catch (e) {
        // swallow; app re-registers on next load
      }
    })(),
  );
});
```

Note: the `pushsubscriptionchange` re-register relies on cookie auth (`credentials: include`) and the device/profile headers it can't set from the SW — it is genuinely best-effort and the app heals it on next load (documented in the spec). `applicationServerKey` accepts a base64url string in modern browsers; if the target browsers need a Uint8Array here, the app-side path (Task 4) converts it — the SW path is the fallback and may no-op on stricter browsers, which is acceptable.

- [ ] **Step 4: App-side helpers — `web/src/lib/push.ts`**

```ts
/** Convert a base64url VAPID public key to the Uint8Array applicationServerKey wants. */
export function urlBase64ToUint8Array(base64: string): Uint8Array {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/** Cache the raw VAPID public key so the SW can re-subscribe without the app. */
export async function cacheVapidKey(base64: string): Promise<void> {
  if (!("caches" in self)) return;
  const cache = await caches.open("silo-push");
  await cache.put("vapid-public-key", new Response(base64));
}

export function pushSupported(): boolean {
  return (
    typeof navigator !== "undefined" &&
    "serviceWorker" in navigator &&
    typeof window !== "undefined" &&
    "PushManager" in window &&
    "Notification" in window
  );
}
```

- [ ] **Step 5: Test the converter — `web/src/lib/push.test.ts`**

```ts
import { describe, expect, it } from "vitest";
import { urlBase64ToUint8Array } from "./push";

describe("urlBase64ToUint8Array", () => {
  it("decodes a url-safe base64 key to bytes", () => {
    // "AQID" → [1,2,3]
    expect(Array.from(urlBase64ToUint8Array("AQID"))).toEqual([1, 2, 3]);
  });
  it("handles url-safe chars and missing padding", () => {
    const out = urlBase64ToUint8Array("a-_9"); // '-'→'+', '_'→'/'
    expect(out.length).toBe(3);
  });
});
```

- [ ] **Step 6: Register the SW — `web/src/main.tsx`**

After `createRoot(...).render(...)`, add:

```tsx
if ("serviceWorker" in navigator && "PushManager" in window) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => {
      // registration failure is non-fatal; push just won't be available
    });
  });
}
```

- [ ] **Step 7: Run, build, commit**

```bash
cd web && pnpm exec vitest run src/lib/pushSw.test.ts src/lib/push.test.ts 2>&1 | tail -5 && pnpm exec tsc -b --noEmit 2>&1 | head -3
git add web/public/sw.js web/src/lib/pushSw.ts web/src/lib/pushSw.test.ts web/src/lib/push.ts web/src/lib/push.test.ts web/src/main.tsx
git commit -m "feat(web): web push service worker, registration, and helpers"
```

---

### Task 3: Push query hooks + types

**Files:**
- Modify: `web/src/api/types.ts`
- Create: `web/src/hooks/queries/push.ts`
- Modify: `web/src/hooks/queries/keys.ts` (add `pushKeys`)

- [ ] **Step 1: Types** — append to `types.ts`:

```ts
export interface PushDeviceInfo {
  device_id: string;
  name: string;
  platform: string;
  transport: string;
  push_enabled: boolean;
  registered_at?: string;
}

export interface VapidKeyPair {
  vapid_public: string;
  vapid_private: string;
}
```

- [ ] **Step 2: Keys** — in `keys.ts`:

```ts
export const pushKeys = {
  all: ["push"] as const,
  webpushKey: () => [...pushKeys.all, "webpush-key"] as const,
  devices: () => [...pushKeys.all, "devices"] as const,
  status: () => [...pushKeys.all, "status"] as const,
};
```

- [ ] **Step 3: Hooks — `web/src/hooks/queries/push.ts`**

Mirror `admin/users.ts` conventions (api import, toast, invalidation).

```ts
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/api/client";
import type { PushDeviceInfo, VapidKeyPair } from "@/api/types";
import { pushKeys } from "@/hooks/queries/keys";

export function useWebPushPublicKey() {
  return useQuery({
    queryKey: pushKeys.webpushKey(),
    queryFn: () => api<{ vapid_public_key: string }>("/notifications/push/webpush-key"),
    select: (d) => d.vapid_public_key,
    staleTime: 5 * 60 * 1000,
  });
}

export function usePushDevices() {
  return useQuery({
    queryKey: pushKeys.devices(),
    queryFn: () => api<{ devices: PushDeviceInfo[] | null }>("/notifications/push/devices"),
    select: (d) => d.devices ?? [],
  });
}

export function useTogglePushDevice() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ deviceId, enabled }: { deviceId: string; enabled: boolean }) =>
      api(`/notifications/push/devices/${deviceId}`, {
        method: "PUT",
        body: JSON.stringify({ enabled }),
      }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: pushKeys.devices() }),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to update device"),
  });
}

export function usePushStatus() {
  return useQuery({
    queryKey: pushKeys.status(),
    queryFn: () => api<{ apns: boolean; fcm: boolean; webpush: boolean }>("/admin/push/status"),
  });
}

export function useGenerateVapidKeys() {
  return useMutation({
    mutationFn: () => api<VapidKeyPair>("/admin/push/generate-vapid-keys", { method: "POST" }),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to generate keys"),
  });
}

export function useSendTestPush() {
  return useMutation({
    mutationFn: () => api("/admin/push/test", { method: "POST" }),
    onSuccess: () => toast.success("Test push queued — watch for a banner"),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to send test"),
  });
}
```

(Register/revoke device calls live inside `usePushDevice` (Task 4) since they're tied to the subscription, not standalone query hooks.)

- [ ] **Step 4: Typecheck + commit**

```bash
cd web && pnpm exec tsc -b --noEmit 2>&1 | head -3
git add web/src/api/types.ts web/src/hooks/queries/keys.ts web/src/hooks/queries/push.ts
git commit -m "feat(web): push query hooks and types"
```

---

### Task 4: `usePushDevice` lifecycle hook

**Files:**
- Create: `web/src/hooks/usePushDevice.ts`, `web/src/hooks/usePushDevice.test.tsx`

- [ ] **Step 1: Failing tests**

Mock `navigator.serviceWorker`, `window.PushManager`, `Notification`, the api client, and the push helpers. Assert the status machine and the enable/disable flows.

```tsx
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { apiMock, cacheVapidMock } = vi.hoisted(() => ({
  apiMock: vi.fn(),
  cacheVapidMock: vi.fn(),
}));
vi.mock("@/api/client", () => ({ api: apiMock }));
vi.mock("@/lib/push", async (orig) => ({
  ...(await orig<typeof import("@/lib/push")>()),
  cacheVapidKey: cacheVapidMock,
}));

import { usePushDevice } from "./usePushDevice";

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
}

let subscribeMock: ReturnType<typeof vi.fn>;
let getSubMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  apiMock.mockReset();
  cacheVapidMock.mockReset();
  subscribeMock = vi.fn(async () => ({ toJSON: () => ({ endpoint: "https://x", keys: {} }) }));
  getSubMock = vi.fn(async () => null);
  (globalThis as any).PushManager = function () {};
  (navigator as any).serviceWorker = {
    register: vi.fn(),
    ready: Promise.resolve({ pushManager: { subscribe: subscribeMock, getSubscription: getSubMock } }),
  };
  (globalThis as any).Notification = { permission: "default", requestPermission: vi.fn(async () => "granted") };
});
afterEach(() => vi.restoreAllMocks());

describe("usePushDevice", () => {
  it("reports unsupported when PushManager missing", () => {
    delete (globalThis as any).PushManager;
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    expect(result.current.status).toBe("unsupported");
  });

  it("reports blocked when permission denied", () => {
    (globalThis as any).Notification.permission = "denied";
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    expect(result.current.status).toBe("blocked");
  });

  it("enable(): permission → key → subscribe → PUT device, caches key, status on", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.includes("webpush-key")) return Promise.resolve({ vapid_public_key: "AQID" });
      return Promise.resolve(undefined); // PUT device
    });
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    await act(async () => { await result.current.enable(); });
    expect(subscribeMock).toHaveBeenCalledOnce();
    expect(cacheVapidMock).toHaveBeenCalledWith("AQID");
    const putCall = apiMock.mock.calls.find((c) => c[0] === "/notifications/push/device");
    expect(putCall?.[1]).toMatchObject({ method: "PUT" });
    expect(JSON.parse(putCall![1].body)).toMatchObject({ transport: "webpush" });
    await waitFor(() => expect(result.current.status).toBe("on"));
  });

  it("enable(): permission denied short-circuits, no subscribe", async () => {
    (globalThis as any).Notification.requestPermission = vi.fn(async () => "denied");
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    await act(async () => { await result.current.enable(); });
    expect(subscribeMock).not.toHaveBeenCalled();
    expect(result.current.status).toBe("blocked");
  });

  it("disable(): unsubscribe + DELETE, status off", async () => {
    const unsub = vi.fn(async () => true);
    getSubMock.mockResolvedValue({ unsubscribe: unsub });
    apiMock.mockResolvedValue(undefined);
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    await act(async () => { await result.current.disable(); });
    expect(unsub).toHaveBeenCalled();
    const del = apiMock.mock.calls.find((c) => c[0] === "/notifications/push/device" && c[1]?.method === "DELETE");
    expect(del).toBeTruthy();
    await waitFor(() => expect(result.current.status).toBe("off"));
  });
});
```

- [ ] **Step 2: Run, verify fail**

```bash
cd web && pnpm exec vitest run src/hooks/usePushDevice.test.tsx 2>&1 | tail -6
```

- [ ] **Step 3: Implement — `web/src/hooks/usePushDevice.ts`**

```ts
import { useCallback, useEffect, useState } from "react";

import { api } from "@/api/client";
import { cacheVapidKey, pushSupported, urlBase64ToUint8Array } from "@/lib/push";

export type PushDeviceStatus = "unsupported" | "blocked" | "off" | "on" | "pending";

export function usePushDevice() {
  const [status, setStatus] = useState<PushDeviceStatus>("pending");

  const refresh = useCallback(async () => {
    if (!pushSupported()) {
      setStatus("unsupported");
      return;
    }
    if (Notification.permission === "denied") {
      setStatus("blocked");
      return;
    }
    try {
      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.getSubscription();
      setStatus(sub ? "on" : "off");
    } catch {
      setStatus("off");
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const enable = useCallback(async () => {
    if (!pushSupported()) {
      setStatus("unsupported");
      return;
    }
    setStatus("pending");
    const perm = await Notification.requestPermission();
    if (perm !== "granted") {
      setStatus(perm === "denied" ? "blocked" : "off");
      return;
    }
    try {
      const reg = await navigator.serviceWorker.ready;
      const { vapid_public_key } = await api<{ vapid_public_key: string }>(
        "/notifications/push/webpush-key",
      );
      if (!vapid_public_key) {
        setStatus("off");
        return;
      }
      await cacheVapidKey(vapid_public_key);
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(vapid_public_key),
      });
      await api("/notifications/push/device", {
        method: "PUT",
        body: JSON.stringify({ transport: "webpush", token: JSON.stringify(sub.toJSON()) }),
      });
      setStatus("on");
    } catch {
      setStatus("off");
    }
  }, []);

  const disable = useCallback(async () => {
    setStatus("pending");
    try {
      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.getSubscription();
      if (sub) await sub.unsubscribe();
    } catch {
      /* best-effort */
    }
    try {
      await api("/notifications/push/device", { method: "DELETE" });
    } catch {
      /* server may already lack the row */
    }
    setStatus("off");
  }, []);

  return { status, enable, disable, refresh };
}
```

- [ ] **Step 4: Run green, typecheck, commit**

```bash
cd web && pnpm exec vitest run src/hooks/usePushDevice.test.tsx 2>&1 | tail -6 && pnpm exec tsc -b --noEmit 2>&1 | head -3
git add web/src/hooks/usePushDevice.ts web/src/hooks/usePushDevice.test.tsx
git commit -m "feat(web): usePushDevice subscription lifecycle hook"
```

---

### Task 5: NotificationsSettings — push control + device list

**Files:**
- Modify: `web/src/pages/settings/NotificationsSettings.tsx`
- Test: extend the page's test if one exists (check `ls web/src/pages/settings/*.test.tsx`)

- [ ] **Step 1: Add a push section**

Read the current `NotificationsSettings.tsx` (it uses `SettingsGroup`/`SettingRow`/`Switch`). Add, below the category-preference group, a new "Push notifications" group:

- A row driven by `usePushDevice()`:
  - `status === "unsupported"` → static text "This browser doesn't support push notifications."
  - `status === "blocked"` → text "Notifications are blocked for this site. Re-enable them in your browser's site settings."
  - else → a `Switch` checked when `status === "on"`, disabled when `status === "pending"`, `onCheckedChange` calls `enable()` / `disable()`.
- Below it, a device list from `usePushDevices()`: one row per device (`name || device_id`, `platform`/`transport`, `registered_at`), each with a `Switch` bound to `useTogglePushDevice().mutate({ deviceId, enabled })` reflecting `push_enabled`. Empty state: "No devices registered for push yet."

Match the page's existing component composition and classes. Keep `content_digest` and the existing toggles untouched.

- [ ] **Step 2: Test (if the page is tested)**

If `NotificationsSettings.test.tsx` exists, mock `usePushDevice` (return `{status:"off", enable, disable}`) and `usePushDevices` (two devices); assert: toggling the device-level switch calls `useTogglePushDevice().mutate` with the device id; the main push switch calls `enable` when off. If no sibling test exists, rely on typecheck + the hook tests and note it in the commit.

- [ ] **Step 3: Typecheck, test, commit**

```bash
cd web && pnpm exec tsc -b --noEmit 2>&1 | head -3 && pnpm exec vitest run src/pages/settings 2>&1 | tail -4
git add web/src/pages/settings/NotificationsSettings.tsx web/src/pages/settings/*.test.tsx
git commit -m "feat(web): push on-this-device control and device list in settings"
```

---

### Task 6: Admin PushSettings page + nav

**Files:**
- Create: `web/src/pages/admin-settings/PushSettings.tsx`
- Modify: `web/src/pages/admin-settings/AdminSettingsLayout.tsx`
- Test: `web/src/pages/admin-settings/PushSettings.test.tsx`

- [ ] **Step 1: Build the page**

Template: `IntegrationsSettings.tsx` (credential card = `CredentialStatus` badge + `SettingField` + `useUpdateServerSetting` + `useAdminSensitiveStatus`). Three cards. Header badge per card from `usePushStatus()`.

- **Web Push card** (leads): fields `push.webpush.vapid_public`, `push.webpush.vapid_private`, `push.webpush.subject`. A "Generate keys" button → `useGenerateVapidKeys().mutateAsync()` fills the public + private field state (local `useState`, not auto-saved). Subject field hint: `mailto:you@example.com`. Each field saves via `updateSetting.mutateAsync({ key, value })`. A "Send test push" button → `useSendTestPush().mutate()`.
- **APNs card**: `push.apns.p8_key` (textarea via `SettingField` multiline if supported, else a `<textarea>`), `push.apns.key_id`, `push.apns.team_id`, `push.apns.bundle_id`.
- **FCM card**: `push.fcm.service_account_json` (textarea).

`configured` per field from `new Set(sensitive?.configured ?? []).has(key)`. Page header + intro like IntegrationsSettings.

- [ ] **Step 2: Register nav**

In `AdminSettingsLayout.tsx`, add to the "Connections" group's `items` (import the component + a lucide icon, e.g. `BellRing`):

```tsx
{ id: "push", label: "Push Notifications", icon: BellRing, component: PushSettings },
```

(`id: "push"` is stable URL state.)

- [ ] **Step 3: Test — `PushSettings.test.tsx`**

Mock `useUpdateServerSetting` (capture mutate args), `useAdminSensitiveStatus` (some keys configured), `usePushStatus` (webpush true), `useGenerateVapidKeys` (returns a pair), `useSendTestPush`. Assert: three cards render; webpush badge shows ready; saving the VAPID public field calls `updateSetting.mutateAsync({ key: "push.webpush.vapid_public", value })`; "Generate keys" populates the public/private fields; "Send test push" calls the mutation. Follow the admin-settings test conventions (check a sibling `*.test.tsx` in that dir; if none, write a minimal RTL test with the mocks).

- [ ] **Step 4: Typecheck, test, build, commit**

```bash
cd web && pnpm exec tsc -b --noEmit 2>&1 | head -3 && pnpm exec vitest run src/pages/admin-settings 2>&1 | tail -5
git add web/src/pages/admin-settings/PushSettings.tsx web/src/pages/admin-settings/PushSettings.test.tsx web/src/pages/admin-settings/AdminSettingsLayout.tsx
git commit -m "feat(web): admin push provider config page"
```

---

### Task 7: Full verification + manual smoke

- [ ] **Step 1: Web gates**

```bash
cd web && pnpm run lint 2>&1 | tail -3 && pnpm run format:check 2>&1 | tail -2 && pnpm exec vitest run 2>&1 | tail -5 && pnpm run build 2>&1 | tail -3
```

All green (lint warnings allowed, errors not). Fix any new-code lint/format issues (prettier --write the new files if format:check flags them). `build` also confirms `public/sw.js` is copied to `dist/` (Vite copies `public/` verbatim).

- [ ] **Step 2: Backend gates**

```bash
GOWORK=off go build ./... && GOWORK=off go test ./internal/api/handlers/ -run TestPush 2>&1 | tail -3 && make verify-local-paths
```

- [ ] **Step 3: Manual browser smoke (documented; requires a real browser over HTTPS or localhost)**

Service workers + Push require a secure context — `localhost` qualifies. Build the server with the web assets and run on a localhost port:

```bash
cd web && pnpm run build && cd ..
GOWORK=off go build -o /tmp/silo-pushweb ./cmd/silo && (PORT=18080 JF_PORT=18096 /tmp/silo-pushweb > /tmp/silo-pushweb.log 2>&1 &)
```

Then in a Chromium browser at `http://localhost:18080`:
1. Log in as admin. Go to Admin → Settings → Push Notifications. Click "Generate keys", Save the VAPID public/private + a `mailto:` subject. Confirm the Web Push badge flips to "Ready" (and `GET /admin/push/status` → webpush:true).
2. Go to Settings → Notifications. Toggle "Push on this device" on → grant the browser permission prompt. Confirm a row appears in the device list and the toggle stays on.
3. Click "Send test push" on the admin page (or create an announcement). Within ~30–45s (grace + worker tick) a desktop notification banner appears. Close the app tab first to prove background delivery.
4. Click the banner → the app opens/focuses at the deep link.
5. Toggle push off → confirm `DELETE` and the device drops from the list.

Record the outcome. (If a real browser isn't available in the harness, run steps 1–2 via curl to confirm the API wiring — register a webpush device, confirm `push_deliveries` row + status — and note the visual steps as operator-verified.)

- [ ] **Step 4: Commit any verification fixes; report.** Work stays on `feat/core-notifications-server` (or a dedicated branch off it).

---

## Self-review notes (resolved during writing)

- `webpush.GenerateVAPIDKeys()` returns `(privateKey, publicKey, err)` in webpush-go — Task 1 Step 1 flags verifying the order so the keys aren't swapped.
- The SW's pure logic is duplicated between `public/sw.js` (untestable static file) and the tested `src/lib/pushSw.ts`; the duplication is small and intentional, with sw.js annotated as mirroring the tested source. The reviewer should diff the two functions for drift.
- `usePushDevice` owns register/revoke (tied to the live subscription); standalone query hooks (`push.ts`) cover read/list/toggle/admin. No overlap.
- Test push reuses `notifications.Service.CreateSystem` (Phase-1) → a real system notification → exercises the full enqueue→worker→transport pipeline to the caller's own devices. No bespoke push path.
- Admin credential writes use the existing `PUT /admin/settings/{key}` (encrypted, redacted) — no new write endpoint; only keygen + test are net-new server surface.
- iOS Safari web push (installed-PWA only) is handled by `pushSupported()` returning false in a normal tab → `unsupported` state; documented, not specially coded.
- Several steps embed verify-greps (NewPushHandler call sites, GenerateVAPIDKeys order, SettingField multiline support, sibling test conventions, the user-id context injection helper in push_test.go) — align with reality.
