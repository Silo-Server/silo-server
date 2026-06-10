import type { ApiError, RefreshResponse } from "./types";
import { storage } from "../utils/storage";

type ProfileUnverifiedListener = () => void;
let profileUnverifiedListener: ProfileUnverifiedListener | null = null;

export function onProfileUnverified(listener: ProfileUnverifiedListener | null) {
  profileUnverifiedListener = listener;
}

let accessToken: string | null = null;
let refreshPromise: Promise<boolean> | null = null;

export function setAccessToken(token: string | null) {
  accessToken = token;
}

export function getAccessToken(): string | null {
  return accessToken;
}

function getRefreshToken(): string | null {
  return storage.get(storage.KEYS.REFRESH_TOKEN);
}

export function setRefreshToken(token: string | null) {
  if (token) {
    storage.set(storage.KEYS.REFRESH_TOKEN, token);
  } else {
    storage.remove(storage.KEYS.REFRESH_TOKEN);
  }
}

function getProfileId(): string | null {
  return storage.get(storage.KEYS.PROFILE_ID);
}

export function setProfileId(id: string | null) {
  if (id) {
    storage.set(storage.KEYS.PROFILE_ID, id);
  } else {
    storage.remove(storage.KEYS.PROFILE_ID);
  }
}

let profileToken: string | null = null;

export function setProfileToken(token: string | null) {
  profileToken = token;
  if (token) {
    storage.set(storage.KEYS.PROFILE_TOKEN, token);
    try {
      sessionStorage.removeItem(storage.KEYS.PROFILE_TOKEN);
    } catch {
      // Storage unavailable
    }
  } else {
    storage.remove(storage.KEYS.PROFILE_TOKEN);
    try {
      sessionStorage.removeItem(storage.KEYS.PROFILE_TOKEN);
    } catch {
      // Storage unavailable
    }
  }
}

export function getProfileToken(): string | null {
  if (!profileToken) {
    profileToken = storage.get(storage.KEYS.PROFILE_TOKEN);
  }
  if (!profileToken) {
    try {
      profileToken = sessionStorage.getItem(storage.KEYS.PROFILE_TOKEN);
      if (profileToken) {
        storage.set(storage.KEYS.PROFILE_TOKEN, profileToken);
        sessionStorage.removeItem(storage.KEYS.PROFILE_TOKEN);
      }
    } catch {
      // Storage unavailable
    }
  }
  return profileToken;
}

function getOrCreateDeviceId(): string | null {
  const existing = storage.get(storage.KEYS.DEVICE_ID);
  if (existing) {
    return existing;
  }

  let nextId: string | null = null;
  try {
    nextId =
      typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : `web-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  } catch {
    nextId = `web-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  }

  if (nextId) {
    storage.set(storage.KEYS.DEVICE_ID, nextId);
  }
  return nextId;
}

function detectDevicePlatform(): string {
  if (typeof navigator === "undefined") {
    return "Web";
  }

  const userAgent = navigator.userAgent.toLowerCase();
  if (/iphone|ipad|ipod/.test(userAgent)) return "iOS Web";
  if (/android/.test(userAgent)) return "Android Web";
  if (/mac os x|macintosh/.test(userAgent)) return "macOS Web";
  if (/windows/.test(userAgent)) return "Windows Web";
  if (/linux/.test(userAgent)) return "Linux Web";
  return "Web";
}

function detectDeviceName(): string {
  if (typeof navigator === "undefined") {
    return "Web Browser";
  }

  const platform = detectDevicePlatform().replace(/\s+Web$/, "");
  let browser = "Browser";
  const userAgent = navigator.userAgent;

  if (/Edg\//.test(userAgent)) browser = "Edge";
  else if (/Chrome\//.test(userAgent) && !/Edg\//.test(userAgent)) browser = "Chrome";
  else if (/Firefox\//.test(userAgent)) browser = "Firefox";
  else if (/Safari\//.test(userAgent) && !/Chrome\//.test(userAgent)) browser = "Safari";

  return `${browser} on ${platform}`;
}

function getDeviceHeaders(): Record<string, string> {
  const deviceId = getOrCreateDeviceId();
  if (!deviceId) {
    return {};
  }

  return {
    "X-Silo-Device-Id": deviceId,
    "X-Silo-Device-Name": detectDeviceName(),
    "X-Silo-Device-Platform": detectDevicePlatform(),
  };
}

async function attemptRefresh(): Promise<boolean> {
  const rt = getRefreshToken();
  if (!rt) return false;

  try {
    const data = await refreshAccessToken(rt, fetch);
    if (!data) return false;
    setAccessToken(data.access_token);
    setRefreshToken(data.refresh_token);
    return true;
  } catch {
    return false;
  }
}

export async function bootstrapAccessToken(fetchImpl: typeof fetch = fetch): Promise<boolean> {
  if (accessToken) {
    return true;
  }

  const rt = getRefreshToken();
  if (!rt) {
    return false;
  }

  try {
    const data = await refreshAccessToken(rt, fetchImpl);
    if (!data) {
      return false;
    }
    setAccessToken(data.access_token);
    setRefreshToken(data.refresh_token);
    return true;
  } catch {
    return false;
  }
}

async function refreshAccessToken(
  refreshToken: string,
  fetchImpl: typeof fetch,
): Promise<RefreshResponse | null> {
  const res = await fetchImpl("/api/v1/auth/refresh", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
  if (!res.ok) {
    return null;
  }
  return res.json();
}

export class ApiClientError extends Error {
  /**
   * Raw parsed JSON body of the error response, when the body parsed as JSON.
   * Carries fields the normalized `details: ApiError` does not surface (e.g.
   * plugin validation `field_errors` / `form_error` on a 400). Undefined for
   * non-JSON or empty bodies.
   */
  public body?: unknown;

  constructor(
    public status: number,
    public code: string,
    message: string,
    public details?: ApiError,
  ) {
    super(message);
    this.name = "ApiClientError";
  }
}

function fallbackApiErrorMessage(res: Response): string {
  const statusText = res.statusText.trim();
  if (statusText) {
    return statusText;
  }
  if (res.status === 401) {
    return "Authentication required.";
  }
  if (res.status === 403) {
    return "You do not have permission to perform this action.";
  }
  if (res.status === 404) {
    return "Requested resource was not found.";
  }
  if (res.status >= 500) {
    return "Request failed. Please try again.";
  }
  if (res.status > 0) {
    return `Request failed (${res.status}).`;
  }
  return "Request failed.";
}

function normalizeApiError(apiErr: Partial<ApiError> | null, res: Response): ApiError {
  const payload = apiErr && typeof apiErr === "object" ? apiErr : {};
  const code =
    typeof payload.error === "string" && payload.error.trim() ? payload.error : "unknown";
  const message =
    typeof payload.message === "string" && payload.message.trim()
      ? payload.message.trim()
      : fallbackApiErrorMessage(res);

  return {
    ...payload,
    error: code,
    message,
  };
}

function hasHeader(headers: Record<string, string>, name: string): boolean {
  const target = name.toLowerCase();
  return Object.keys(headers).some((key) => key.toLowerCase() === target);
}

interface ParsedApiError {
  /** Normalized error with guaranteed `error`/`message` fields. */
  apiErr: ApiError;
  /** Raw parsed JSON body, or undefined when the body wasn't JSON/empty. */
  raw?: unknown;
}

async function parseApiError(res: Response): Promise<ParsedApiError> {
  let apiErr: Partial<ApiError> = {};
  let raw: unknown;
  try {
    raw = await res.json();
    if (raw && typeof raw === "object") {
      apiErr = raw as Partial<ApiError>;
    }
  } catch {
    // response wasn't JSON
  }
  return { apiErr: normalizeApiError(apiErr, res), raw };
}

/** Builds an ApiClientError from a parsed error response, attaching the raw body. */
function apiClientErrorFrom(status: number, parsed: ParsedApiError): ApiClientError {
  const err = new ApiClientError(status, parsed.apiErr.error, parsed.apiErr.message, parsed.apiErr);
  err.body = parsed.raw;
  return err;
}

export interface RestoredUserSession<TUser> {
  user: TUser;
  accessToken: string;
  refreshToken: string;
}

export async function restoreUserSession<TUser>({
  accessToken,
  refreshToken,
  fetchImpl = fetch,
}: {
  accessToken: string;
  refreshToken: string;
  fetchImpl?: typeof fetch;
}): Promise<RestoredUserSession<TUser>> {
  let restoredAccessToken = accessToken;
  let restoredRefreshToken = refreshToken;

  const requestUser = (token: string) =>
    fetchImpl("/api/v1/auth/me", {
      headers: {
        Authorization: `Bearer ${token}`,
      },
    });

  let res = await requestUser(restoredAccessToken);

  if (res.status === 401) {
    const refreshed = await refreshAccessToken(restoredRefreshToken, fetchImpl);
    if (refreshed) {
      restoredAccessToken = refreshed.access_token;
      restoredRefreshToken = refreshed.refresh_token;
      res = await requestUser(restoredAccessToken);
    }
  }

  if (!res.ok) {
    throw apiClientErrorFrom(res.status, await parseApiError(res));
  }

  return {
    user: (await res.json()) as TUser,
    accessToken: restoredAccessToken,
    refreshToken: restoredRefreshToken,
  };
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = buildApiHeaders(options);

  let res = await fetch(`/api/v1${path}`, { ...options, headers });

  // Auto-refresh on 401
  if (res.status === 401 && getRefreshToken()) {
    if (!refreshPromise) {
      refreshPromise = attemptRefresh().finally(() => {
        refreshPromise = null;
      });
    }
    const refreshed = await refreshPromise;
    if (refreshed) {
      headers["Authorization"] = `Bearer ${accessToken}`;
      res = await fetch(`/api/v1${path}`, { ...options, headers });
    }
  }

  if (!res.ok) {
    const parsed = await parseApiError(res);
    if (res.status === 403 && parsed.apiErr.error === "profile_unverified") {
      setProfileToken(null);
      profileUnverifiedListener?.();
    }
    throw apiClientErrorFrom(res.status, parsed);
  }

  // Handle empty successful responses.
  if (res.status === 204 || res.status === 205) {
    return undefined as T;
  }
  const text = await res.text();
  if (text.trim() === "") {
    return undefined as T;
  }
  return JSON.parse(text) as T;
}

function buildApiHeaders(options: RequestInit = {}): Record<string, string> {
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  };
  if (!(options.body instanceof FormData) && !hasHeader(headers, "Content-Type")) {
    headers["Content-Type"] = "application/json";
  }
  if (accessToken) {
    headers["Authorization"] = `Bearer ${accessToken}`;
  }
  const profileId = getProfileId();
  if (profileId) {
    headers["X-Profile-Id"] = profileId;
  }
  const profToken = getProfileToken();
  if (profToken) {
    headers["X-Profile-Token"] = profToken;
  }
  Object.assign(headers, getDeviceHeaders());
  return headers;
}

/**
 * Fire-and-forget API request that survives page unload (pagehide / tab close).
 * Sends the same auth, profile, and device headers as `api`, plus `keepalive`
 * so the browser finishes the request after the document is gone. The response
 * is intentionally ignored: no token refresh or error handling is possible
 * while the page is unloading.
 */
export function apiKeepalive(path: string, options: RequestInit = {}): void {
  const headers = buildApiHeaders(options);
  void fetch(`/api/v1${path}`, { ...options, headers, keepalive: true }).catch(() => {
    // Best-effort write during unload; nothing left to recover into.
  });
}

/** Downloads a binary API response and triggers a browser file save. */
export async function apiDownload(
  path: string,
  filename: string,
  options: RequestInit = {},
): Promise<void> {
  let headers = buildApiHeaders(options);
  let res = await fetch(`/api/v1${path}`, { ...options, headers });

  if (res.status === 401 && getRefreshToken()) {
    if (!refreshPromise) {
      refreshPromise = attemptRefresh().finally(() => {
        refreshPromise = null;
      });
    }
    const refreshed = await refreshPromise;
    if (refreshed) {
      headers = buildApiHeaders(options);
      headers["Authorization"] = `Bearer ${accessToken}`;
      res = await fetch(`/api/v1${path}`, { ...options, headers });
    }
  }

  if (!res.ok) {
    throw apiClientErrorFrom(res.status, await parseApiError(res));
  }

  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
}

/**
 * apiBlob buffers the entire response body in memory, so cap what it will
 * accept; beyond this a download is the right tool, not an in-tab blob.
 */
export const API_BLOB_MAX_BYTES = 512 * 1024 * 1024;

export async function apiBlob(path: string, options: RequestInit = {}): Promise<Blob> {
  let headers = buildApiHeaders(options);
  let res = await fetch(`/api/v1${path}`, { ...options, headers });

  if (res.status === 401 && getRefreshToken()) {
    if (!refreshPromise) {
      refreshPromise = attemptRefresh().finally(() => {
        refreshPromise = null;
      });
    }
    const refreshed = await refreshPromise;
    if (refreshed) {
      headers = buildApiHeaders(options);
      headers["Authorization"] = `Bearer ${accessToken}`;
      res = await fetch(`/api/v1${path}`, { ...options, headers });
    }
  }

  if (!res.ok) {
    throw apiClientErrorFrom(res.status, await parseApiError(res));
  }

  // Reject oversized bodies up front instead of crashing the tab while
  // buffering them. When the header is absent, proceed; streaming byte counts
  // are not worth the complexity here.
  const contentLength = Number(res.headers.get("Content-Length"));
  if (Number.isFinite(contentLength) && contentLength > API_BLOB_MAX_BYTES) {
    const sizeMiB = Math.round(contentLength / (1024 * 1024));
    const limitMiB = Math.round(API_BLOB_MAX_BYTES / (1024 * 1024));
    throw new ApiClientError(
      res.status,
      "response_too_large",
      `This file is too large to open in the browser (${sizeMiB} MiB, limit ${limitMiB} MiB). Download it instead.`,
    );
  }

  return res.blob();
}

// People API
export async function searchPeople(query: string, limit = 20): Promise<import("./types").Person[]> {
  const params = new URLSearchParams({ q: query, limit: String(limit) });
  return api<import("./types").Person[]>(`/people?${params}`);
}

export async function getPerson(id: string): Promise<import("./types").Person> {
  return api<import("./types").Person>(`/people/${id}`);
}

export async function refreshPerson(
  id: string,
): Promise<import("./types").PersonRefreshQueuedResponse> {
  return api<import("./types").PersonRefreshQueuedResponse>(`/people/${id}/refresh`, {
    method: "POST",
  });
}

export async function adminRefreshPerson(id: string): Promise<import("./types").Person> {
  return api<import("./types").Person>(`/admin/people/${id}/refresh`, {
    method: "POST",
  });
}

export async function adminUpdatePerson(
  id: string,
  data: import("./types").UpdatePersonRequest,
): Promise<import("./types").Person> {
  return api<import("./types").Person>(`/admin/people/${id}`, {
    method: "PATCH",
    body: JSON.stringify(data),
  });
}

export async function getPersonCatalogItems(
  id: string,
  type?: string,
  limit = 24,
  offset = 0,
): Promise<import("./types").BrowseResponse> {
  const params = new URLSearchParams({
    source: "person",
    person_id: id,
    limit: String(limit),
    offset: String(offset),
  });
  if (type) params.set("type", type);
  return api<import("./types").BrowseResponse>(`/catalog?${params}`);
}
