import type { ApiError, RefreshResponse } from "./types";
import { storage } from "../utils/storage";
import { randomUUID } from "../lib/uuid";

type ProfileUnverifiedListener = () => void;
let profileUnverifiedListener: ProfileUnverifiedListener | null = null;

export function onProfileUnverified(listener: ProfileUnverifiedListener | null) {
  profileUnverifiedListener = listener;
}

let accessToken: string | null = null;
let authContextVersion = 0;
let pendingRefresh: {
  authContextVersion: number;
  serverOrigin: string;
  promise: Promise<boolean>;
} | null = null;

export function setAccessToken(token: string | null) {
  if (accessToken !== token) authContextVersion += 1;
  accessToken = token;
}

function refreshCurrentAccessToken(token: string): void {
  // Token rotation preserves the authenticated account/server context. A
  // queued request may use its captured predecessor once, safely refresh, and
  // retry with this successor without changing account authority.
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

/**
 * Complete request authority for one queued profile intent. It is deliberately
 * an in-memory value: access and PIN tokens must never enter storage, query
 * caches, logs, or persisted mutation state through this snapshot.
 */
export interface ProfileRequestContextSnapshot {
  accessToken: string;
  authContextVersion: number;
  serverOrigin: string;
  profileId: string;
  profileToken: string | null;
}

function currentServerOrigin(): string {
  return typeof globalThis.location === "undefined" ? "" : globalThis.location.origin;
}

/** Capture account, server, profile, and PIN authority in one synchronous turn. */
export function captureProfileRequestContext(): ProfileRequestContextSnapshot | null {
  const profileId = getProfileId();
  if (!accessToken || !profileId) return null;
  return {
    accessToken,
    authContextVersion,
    serverOrigin: currentServerOrigin(),
    profileId,
    profileToken: getProfileToken(),
  };
}

/**
 * A session-authority change advances authContextVersion even if an account
 * later happens to return to the same token. Queued work can therefore never
 * cross a logout, impersonation, account switch, or server-origin switch
 * unnoticed. Automatic token rotation deliberately preserves the context.
 * Profile/PIN changes are excluded so an already-created intent remains bound
 * to its captured profile authority through a same-account refresh.
 */
export function isProfileRequestContextCurrent(snapshot: ProfileRequestContextSnapshot): boolean {
  return (
    snapshot.authContextVersion === authContextVersion &&
    snapshot.serverOrigin === currentServerOrigin()
  );
}

/**
 * Whether the captured profile is still the active one. Unlike
 * isProfileRequestContextCurrent this does compare the profile id and PIN
 * token, so callers deciding whether a completed write should touch
 * profile-scoped caches can tell a household profile switch apart from a
 * same-account token refresh.
 */
export function isCapturedProfileAuthorityActive(snapshot: ProfileRequestContextSnapshot): boolean {
  return (
    isProfileRequestContextCurrent(snapshot) &&
    getProfileId() === snapshot.profileId &&
    getProfileToken() === snapshot.profileToken
  );
}

export function getOrCreateDeviceId(): string {
  const existing = storage.get(storage.KEYS.DEVICE_ID);
  if (existing) {
    return existing;
  }

  const nextId = randomUUID();
  storage.set(storage.KEYS.DEVICE_ID, nextId);
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
  return {
    "X-Silo-Device-Id": deviceId,
    "X-Silo-Device-Name": detectDeviceName(),
    "X-Silo-Device-Platform": detectDevicePlatform(),
    // Browser preferences roam between browsers without changing TV, mobile,
    // tablet, or desktop-native layouts.
    "X-Silo-Client-Family": "web",
  };
}

/** Share one token rotation across player and ordinary API requests. */
export function refreshAuthentication(): Promise<boolean> {
  const serverOrigin = currentServerOrigin();
  if (
    pendingRefresh?.authContextVersion === authContextVersion &&
    pendingRefresh.serverOrigin === serverOrigin
  ) {
    return pendingRefresh.promise;
  }
  const promise = attemptRefresh().finally(() => {
    if (pendingRefresh?.promise === promise) pendingRefresh = null;
  });
  pendingRefresh = { authContextVersion, serverOrigin, promise };
  return promise;
}

export function getAuthContextVersion(): number {
  return authContextVersion;
}

async function attemptRefresh(): Promise<boolean> {
  const rt = getRefreshToken();
  if (!rt) return false;

  // A refresh response belongs only to the account/server that started it.
  // Discarding it after a context switch prevents a delayed old-account
  // response from overwriting the new account's access or refresh token.
  const startingAuthContextVersion = authContextVersion;
  const startingServerOrigin = currentServerOrigin();

  try {
    const data = await refreshAccessToken(rt, fetch);
    if (!data) return false;
    if (
      startingAuthContextVersion !== authContextVersion ||
      startingServerOrigin !== currentServerOrigin()
    ) {
      return false;
    }
    refreshCurrentAccessToken(data.access_token);
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

function setHeader(headers: Record<string, string>, name: string, value: string): void {
  const target = name.toLowerCase();
  for (const key of Object.keys(headers)) {
    if (key.toLowerCase() === target) delete headers[key];
  }
  headers[name] = value;
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

async function readApiResponse<T>(res: Response): Promise<T> {
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

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  return readApiResponse<T>(await apiResponse(path, options));
}

/**
 * Sends a request with one captured account/profile authority. The explicit
 * headers cannot be replaced by the current session, and a stale snapshot is
 * rejected before fetch.
 */
export async function apiWithProfileRequestContext<T>(
  path: string,
  snapshot: ProfileRequestContextSnapshot,
  options: RequestInit = {},
): Promise<T> {
  if (!isProfileRequestContextCurrent(snapshot)) {
    throw new StaleApiRequestContextError();
  }
  const headers = { ...(options.headers as Record<string, string>) };
  setHeader(headers, "Authorization", `Bearer ${snapshot.accessToken}`);
  setHeader(headers, "X-Profile-Id", snapshot.profileId);
  setHeader(headers, "X-Profile-Token", snapshot.profileToken ?? "");
  const response = await apiResponseInternal(path, { ...options, headers }, snapshot);
  if (!isProfileRequestContextCurrent(snapshot)) {
    throw new StaleApiRequestContextError();
  }
  return readApiResponse<T>(response);
}

export class StaleApiRequestContextError extends Error {
  constructor() {
    super("The account or server changed before the queued request could be sent.");
    this.name = "StaleApiRequestContextError";
  }
}

/** Performs an authenticated API request while leaving the successful body unread. */
export async function apiResponse(path: string, options: RequestInit = {}): Promise<Response> {
  return apiResponseInternal(path, options);
}

async function apiResponseInternal(
  path: string,
  options: RequestInit,
  snapshot?: ProfileRequestContextSnapshot,
): Promise<Response> {
  if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
    throw new StaleApiRequestContextError();
  }
  const explicitAuthorization = hasHeader(
    (options.headers as Record<string, string> | undefined) ?? {},
    "Authorization",
  );
  const headers = buildApiHeaders(options);
  const requestProfileId = headers["X-Profile-Id"] ?? null;
  const requestProfileToken = headers["X-Profile-Token"] ?? null;

  let res = await fetch(`/api/v1${path}`, { ...options, headers });

  if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
    throw new StaleApiRequestContextError();
  }

  // Auto-refresh on 401. An ordinary explicit Authorization header opts out,
  // but a captured profile request is a stronger contract: it may rotate the
  // token only while its account/server generation remains current, then retry
  // with the new access token and the exact captured profile/PIN headers.
  if (
    res.status === 401 &&
    getRefreshToken() &&
    (snapshot !== undefined || !explicitAuthorization)
  ) {
    if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
      throw new StaleApiRequestContextError();
    }
    const refreshed = await refreshAuthentication();
    if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
      throw new StaleApiRequestContextError();
    }
    if (refreshed) {
      // Keep the profile and device identity captured for the original
      // request. A household profile can change while refresh is pending;
      // rebuilding every header here could replay an old-profile mutation
      // under the newly selected profile. Only the refreshed account token
      // is allowed to change for this retry.
      const refreshedHeaders = { ...headers };
      if (accessToken) {
        setHeader(refreshedHeaders, "Authorization", `Bearer ${accessToken}`);
      } else if (snapshot) {
        throw new StaleApiRequestContextError();
      } else {
        delete refreshedHeaders.Authorization;
      }
      res = await fetch(`/api/v1${path}`, { ...options, headers: refreshedHeaders });
      if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
        throw new StaleApiRequestContextError();
      }
    }
  }

  if (!res.ok) {
    const parsed = await parseApiError(res);
    if (
      res.status === 403 &&
      parsed.apiErr.error === "profile_unverified" &&
      (snapshot
        ? isCapturedProfileAuthorityActive(snapshot)
        : getProfileId() === requestProfileId && getProfileToken() === requestProfileToken)
    ) {
      setProfileToken(null);
      profileUnverifiedListener?.();
    }
    throw apiClientErrorFrom(res.status, parsed);
  }
  return res;
}

function buildApiHeaders(options: RequestInit = {}): Record<string, string> {
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  };
  if (!(options.body instanceof FormData) && !hasHeader(headers, "Content-Type")) {
    headers["Content-Type"] = "application/json";
  }
  if (accessToken && !hasHeader(headers, "Authorization")) {
    headers["Authorization"] = `Bearer ${accessToken}`;
  }
  const profileId = getProfileId();
  if (profileId && !hasHeader(headers, "X-Profile-Id")) {
    headers["X-Profile-Id"] = profileId;
  }
  const profToken = getProfileToken();
  if (profToken && !hasHeader(headers, "X-Profile-Token")) {
    headers["X-Profile-Token"] = profToken;
  }
  for (const [name, value] of Object.entries(getDeviceHeaders())) {
    setHeader(headers, name, value);
  }
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
  const res = await apiResponse(path, options);

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
  const res = await apiResponse(path, options);

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
