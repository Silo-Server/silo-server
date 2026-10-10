import type { ApiError } from "./types";
import type { components } from "./v2/schema";
import { storage } from "../utils/storage";
import { randomUUID } from "../lib/uuid";
import { problemId } from "./v2/problemId";
import { API_READ_TIMEOUT_MS, startRequestDeadline } from "./requestDeadline";

type ProfileUnverifiedListener = () => void;
let profileUnverifiedListener: ProfileUnverifiedListener | null = null;

export function onProfileUnverified(listener: ProfileUnverifiedListener | null) {
  profileUnverifiedListener = listener;
}

type SessionRejectedListener = () => void;
let sessionRejectedListener: SessionRejectedListener | null = null;

/**
 * Registers the handler for a signed-in session the server stopped accepting
 * (the account was disabled or the session revoked): a refresh the server
 * refused while an access token was in use. The handler ends the session.
 */
export function onSessionRejected(listener: SessionRejectedListener | null) {
  sessionRejectedListener = listener;
}

type RoleChangedListener = () => void;
let roleChangedListener: RoleChangedListener | null = null;

/**
 * Registers the handler for an access token the server refused because the
 * account's role changed (401 `token_refresh_required`). The request has
 * already been refreshed and retried by then; the handler re-reads the
 * account so role-gated UI follows the new role.
 */
export function onRoleChanged(listener: RoleChangedListener | null) {
  roleChangedListener = listener;
}

/** The problem id of a 401 Problem Details response, or null. */
async function unauthorizedProblemId(res: Response): Promise<string | null> {
  if (res.status !== 401) return null;
  try {
    const body = (await res.clone().json()) as { type?: unknown };
    return typeof body.type === "string" ? problemId({ type: body.type }) : null;
  } catch {
    return null;
  }
}

/**
 * Whether a refused refresh means the server will never accept this session
 * again: 401 `session_expired`, sent for a session that was revoked or expired
 * or whose account was disabled or deleted. The server also answers its own
 * failures (a database error, say) with 401 `invalid_token`, so no other
 * refusal ends a session that is in use. A request refused with
 * `token_refresh_required` is refreshed and retried like any other 401; only
 * the refresh's own answer can end the session.
 */
async function isSessionRejection(res: Response): Promise<boolean> {
  return (await unauthorizedProblemId(res)) === "session_expired";
}

/** The refresh whose role change was already reported, so concurrent requests report it once. */
let reportedRoleChangeRefresh: Promise<boolean> | null = null;

/** Whether a failed refresh is the server's 503 `provider_unavailable`. */
async function isProviderOutage(res: Response): Promise<boolean> {
  if (res.status !== 503) return false;
  try {
    const body = (await res.clone().json()) as { type?: unknown };
    return (
      typeof body.type === "string" && problemId({ type: body.type }) === "provider_unavailable"
    );
  } catch {
    return false;
  }
}

let accessToken: string | null = null;
let authContextVersion = 0;
/**
 * How one refresh exchange ended. `rejected`: the server refused it.
 * `unavailable`: no verdict on the session arrived (no network, the
 * deadline, a 5xx, 408 or 429). `superseded`: the account or server changed
 * while it ran, so its answer was discarded.
 */
type RefreshOutcome = "refreshed" | "rejected" | "unavailable" | "superseded";

let pendingRefresh: {
  authContextVersion: number;
  serverOrigin: string;
  outcome: Promise<RefreshOutcome>;
  promise: Promise<boolean>;
} | null = null;
/**
 * The boot-time restore of a stored session: the first exchange of the
 * persisted refresh token for an access token. Requests sent while it is in
 * flight wait for it instead of going out anonymous and coming back 401.
 */
let sessionRestore: Promise<boolean> | null = null;

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
  return sharedRefresh().promise;
}

/** The in-flight refresh for this account and server, started if there is none. */
function sharedRefresh(): { outcome: Promise<RefreshOutcome>; promise: Promise<boolean> } {
  const serverOrigin = currentServerOrigin();
  if (
    pendingRefresh?.authContextVersion === authContextVersion &&
    pendingRefresh.serverOrigin === serverOrigin
  ) {
    return pendingRefresh;
  }
  const outcome = attemptRefresh().finally(() => {
    if (pendingRefresh?.outcome === outcome) pendingRefresh = null;
  });
  const promise = outcome.then((result) => result === "refreshed");
  pendingRefresh = { authContextVersion, serverOrigin, outcome, promise };
  return pendingRefresh;
}

/**
 * A request met a 401, and the token refresh that should answer it got no
 * verdict from the server (no network, the deadline, a 5xx). The session may
 * still be good, so the request fails like a network error rather than with
 * the 401, which would read as the server refusing the session.
 */
export class SessionRefreshUnavailableError extends Error {
  constructor() {
    super("The session could not be refreshed because the server did not answer.");
    this.name = "SessionRefreshUnavailableError";
  }
}

/**
 * Waits for `promise` unless `signal` aborts first, in which case it rejects
 * with the signal's reason. The promise itself keeps running, so a shared
 * refresh still completes for the other requests waiting on it.
 */
function untilAborted<T>(promise: Promise<T>, signal: AbortSignal | null | undefined): Promise<T> {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise<T>((resolve, reject) => {
    const onAbort = () => reject(signal.reason);
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(
      (value) => {
        signal.removeEventListener("abort", onAbort);
        resolve(value);
      },
      (error: unknown) => {
        signal.removeEventListener("abort", onAbort);
        reject(error);
      },
    );
  });
}

export function getAuthContextVersion(): number {
  return authContextVersion;
}

/**
 * A refresh the server did not refuse: a 5xx (a fail_closed provider outage
 * answers 503 provider_unavailable and says the session stays valid), a
 * timeout, a rate limit or no network. The stored session may still work
 * once it passes, so these must not discard it.
 */
function isTransientRefreshFailure(status: number): boolean {
  return status >= 500 || status === 408 || status === 429;
}

let lastRefreshTransient = false;
let lastRefreshProviderOutage = false;

/**
 * Whether the latest failed refresh failed for a reason that may pass
 * (isTransientRefreshFailure) instead of the server refusing the session.
 * The boot restore keeps the stored refresh token then.
 */
export function lastRefreshFailureWasTransient(): boolean {
  return lastRefreshTransient;
}

/**
 * Whether the latest failed refresh was the server's 503
 * `provider_unavailable`: the session's sign-in provider could not be
 * reached for its re-check. Other transient failures (the server itself, the
 * network) are not the provider's.
 */
export function lastRefreshFailureWasProviderOutage(): boolean {
  return lastRefreshProviderOutage;
}

async function attemptRefresh(): Promise<RefreshOutcome> {
  const rt = getRefreshToken();
  if (!rt) return "rejected";

  // A refresh response belongs only to the account/server that started it.
  // Discarding it after a context switch prevents a delayed old-account
  // response from overwriting the new account's access or refresh token.
  const startingAuthContextVersion = authContextVersion;
  const startingServerOrigin = currentServerOrigin();
  const hadAccessToken = accessToken !== null;
  let sessionRejected = false;
  let transient = false;
  let providerOutage = false;
  // Every request that meets a 401 joins this one refresh, so a server that
  // stops answering it must not hold them all forever. Abandoning the
  // exchange is safe: the server does not spend a refresh token on use.
  const deadline = startRequestDeadline(
    API_READ_TIMEOUT_MS,
    () => new DOMException("The token refresh timed out", "TimeoutError"),
  );

  try {
    const data = await refreshAccessToken(rt, async (input, init) => {
      const res = await fetch(input, { ...init, signal: deadline.signal });
      if (!res.ok) {
        sessionRejected = await isSessionRejection(res);
        transient = isTransientRefreshFailure(res.status);
        providerOutage = await isProviderOutage(res);
      }
      return res;
    });
    if (
      startingAuthContextVersion !== authContextVersion ||
      startingServerOrigin !== currentServerOrigin()
    ) {
      return "superseded";
    }
    // A refusal is only a verdict once its answer arrived whole. If the
    // deadline cut the exchange short (headers in, problem body stalled),
    // whatever was read of it decides nothing about the session.
    const noVerdict = transient || deadline.expired;
    lastRefreshTransient = !data && noVerdict;
    lastRefreshProviderOutage = !data && providerOutage;
    if (!data) {
      // Only a mid-session refusal ends the session here. The boot restore
      // (no access token yet) clears its own tokens, and a server error or
      // outage may pass, so neither signs the user out. The refresh token is
      // shared across tabs: when another tab has already stored a new one,
      // this refusal is about a session that tab replaced.
      if (hadAccessToken && sessionRejected && getRefreshToken() === rt) {
        sessionRejectedListener?.();
      }
      return noVerdict ? "unavailable" : "rejected";
    }
    if (accessToken === null) {
      // Nothing to rotate: this exchange establishes the session (the boot
      // restore), which changes the client's authority the way a login does.
      setAccessToken(data.access_token);
    } else {
      refreshCurrentAccessToken(data.access_token);
    }
    setRefreshToken(data.refresh_token);
    return "refreshed";
  } catch {
    // No answer at all (the network, a body that did not parse): the
    // session was not refused.
    lastRefreshTransient = true;
    lastRefreshProviderOutage = false;
    return "unavailable";
  } finally {
    deadline.dispose();
  }
}

/**
 * Restores the stored session at boot; resolves true once an access token is
 * available. It runs on the refresh single-flight, so a request that meets a
 * 401 meanwhile joins this exchange instead of spending the refresh token a
 * second time.
 */
export function bootstrapAccessToken(): Promise<boolean> {
  if (accessToken) return Promise.resolve(true);
  if (sessionRestore) return sessionRestore;
  if (!getRefreshToken()) return Promise.resolve(false);
  const restore = refreshAuthentication().finally(() => {
    if (sessionRestore === restore) sessionRestore = null;
  });
  sessionRestore = restore;
  return restore;
}

/** The tokens the v2 refreshSession operation answers with. */
export type RefreshedTokens = components["schemas"]["RefreshedTokens"];

/**
 * Rotates a refresh token through the v2 refreshSession operation. This is
 * the one v2 request issued outside the typed boundary: it runs underneath
 * `fetchWithSession`, so it cannot import that boundary without a cycle. A
 * non-2xx answer is `null`: the caller tells a refusal (401 session_expired
 * or invalid_token) from an outage (5xx, such as 503 provider_unavailable)
 * by the response status, and only a refusal clears the session.
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
  // Wait out a boot-time session restore so the request carries the restored
  // token. A request that brings its own authority does not depend on it.
  // The wait follows the request's signal, so a caller's deadline or abort
  // still ends it while the restore runs on for everyone else.
  if (sessionRestore && !accessToken && !explicitAuthorization && !snapshot) {
    await untilAborted(sessionRestore, options.signal);
  }
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
    // Join the shared refresh before reading the body: an await in between
    // lets a concurrent refresh finish first, and this request would then
    // start a second one against whatever session replaced it.
    const { outcome, promise: refresh } = sharedRefresh();
    // The refresh is shared and has its own deadline; this request stops
    // waiting for it when its own signal aborts, without cancelling it.
    const [problem, refreshOutcome] = await untilAborted(
      Promise.all([unauthorizedProblemId(res), outcome]),
      options.signal,
    );
    const refreshed = refreshOutcome === "refreshed";
    const roleChanged = problem === "token_refresh_required";
    if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
      throw new StaleApiRequestContextError();
    }
    if (refreshOutcome === "unavailable") {
      throw new SessionRefreshUnavailableError();
    }
    if (refreshed && roleChanged && reportedRoleChangeRefresh !== refresh) {
      reportedRoleChangeRefresh = refresh;
      roleChangedListener?.();
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
