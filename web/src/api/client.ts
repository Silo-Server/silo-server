import type { ApiError } from "./types";
import type { components } from "./v2/schema";
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

// Restore once during module initialization, before query/render consumers can
// capture authority. Reading a snapshot must not mutate proof state or storage.
function restoreProfileToken(): string | null {
  const persisted = storage.get(storage.KEYS.PROFILE_TOKEN);
  if (persisted) return persisted;
  let legacy: string | null;
  try {
    legacy = sessionStorage.getItem(storage.KEYS.PROFILE_TOKEN);
  } catch {
    return null;
  }
  if (legacy) {
    storage.set(storage.KEYS.PROFILE_TOKEN, legacy);
    try {
      sessionStorage.removeItem(storage.KEYS.PROFILE_TOKEN);
    } catch {
      // Migration cleanup must not discard a successfully restored proof.
    }
  }
  return legacy;
}

let profileToken: string | null = restoreProfileToken();
let profileTokenGeneration = 0;

/**
 * Non-secret, process-local PIN authority generation for query keys. It must
 * accompany account/server/profile identity; it is not a credential or a
 * replacement for full captured-authority checks. Reads never advance it.
 */
export function getProfileTokenGeneration(): number {
  return profileTokenGeneration;
}

export function setProfileToken(token: string | null) {
  // Every explicit proof installation or removal starts a new cache generation,
  // including reinstalling the same proof after an intervening removal.
  profileTokenGeneration += 1;
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
  /** Present on client captures; optional for existing caller-supplied snapshots. */
  profileTokenGeneration?: number;
}

function currentServerOrigin(): string {
  return typeof globalThis.location === "undefined" ? "" : globalThis.location.origin;
}

/** Non-secret identity fence, including while the browser is signed out. */
export interface SessionIdentitySnapshot {
  authContextVersion: number;
  serverOrigin: string;
}

export function captureSessionIdentity(): SessionIdentitySnapshot {
  return { authContextVersion, serverOrigin: currentServerOrigin() };
}

export function isSessionIdentityCurrent(snapshot: SessionIdentitySnapshot): boolean {
  return (
    snapshot.authContextVersion === authContextVersion &&
    snapshot.serverOrigin === currentServerOrigin()
  );
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
    profileTokenGeneration: getProfileTokenGeneration(),
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

/** The tokens the v2 refreshSession operation answers with. */
export type RefreshedTokens = components["schemas"]["RefreshedTokens"];

/**
 * Rotates a refresh token through the v2 refreshSession operation. This is
 * the one v2 request issued outside the typed boundary: it runs underneath
 * `fetchWithSession`, so it cannot import that boundary without a cycle. A
 * non-2xx answer (a revoked or malformed token) is `null`; the caller clears
 * the session.
 */
export async function refreshAccessToken(
  refreshToken: string,
  fetchImpl: typeof fetch,
): Promise<RefreshedTokens | null> {
  const res = await fetchImpl("/api/v2/auth/refresh", {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
  if (!res.ok) {
    return null;
  }
  return (await res.json()) as RefreshedTokens;
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

export class StaleApiRequestContextError extends Error {
  constructor() {
    super("The account or server changed before the queued request could be sent.");
    this.name = "StaleApiRequestContextError";
  }
}

/** The response of one session-bound fetch plus the profile identity it carried. */
export interface SessionFetchResult {
  res: Response;
  requestProfileId: string | null;
  requestProfileToken: string | null;
}

/**
 * Sends one request with the current account, profile, and device headers and
 * retries once after a token refresh on 401. The URL is complete and the
 * caller owns the status and body handling.
 *
 * Shared by the v2 request boundary; not for direct use at call sites.
 */
export async function fetchWithSession(
  url: string,
  options: RequestInit,
  snapshot?: ProfileRequestContextSnapshot,
  retryAuthentication = true,
): Promise<SessionFetchResult> {
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

  let res = await fetch(url, { ...options, headers });

  if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
    throw new StaleApiRequestContextError();
  }

  // Auto-refresh on 401. An ordinary explicit Authorization header opts out,
  // but a captured profile request is a stronger contract: it may rotate the
  // token only while its account/server generation remains current, then retry
  // with the new access token and the exact captured profile/PIN headers.
  if (
    res.status === 401 &&
    retryAuthentication &&
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
      res = await fetch(url, { ...options, headers: refreshedHeaders });
      if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
        throw new StaleApiRequestContextError();
      }
    }
  }

  return { res, requestProfileId, requestProfileToken };
}

/**
 * Drops the active PIN token and notifies the profile-unverified listener when
 * the server rejected the profile authority that is still the active one. A
 * rejection for a profile the user has since switched away from is ignored.
 */
export function reportProfileUnverified(
  requestProfileId: string | null,
  requestProfileToken: string | null,
  snapshot?: ProfileRequestContextSnapshot,
): void {
  const stillActive = snapshot
    ? isCapturedProfileAuthorityActive(snapshot)
    : getProfileId() === requestProfileId && getProfileToken() === requestProfileToken;
  if (stillActive) {
    setProfileToken(null);
    profileUnverifiedListener?.();
  }
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

export const API_BLOB_MAX_BYTES = 512 * 1024 * 1024;
