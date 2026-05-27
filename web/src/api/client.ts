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

async function parseApiError(res: Response): Promise<ApiError> {
  let apiErr: Partial<ApiError> = {};
  try {
    apiErr = await res.json();
  } catch {
    // response wasn't JSON
  }
  return normalizeApiError(apiErr, res);
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
    const apiErr = await parseApiError(res);
    throw new ApiClientError(res.status, apiErr.error, apiErr.message, apiErr);
  }

  return {
    user: (await res.json()) as TUser,
    accessToken: restoredAccessToken,
    refreshToken: restoredRefreshToken,
  };
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  };
  if (!(options.body instanceof FormData)) {
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
    const apiErr = await parseApiError(res);
    if (res.status === 403 && apiErr.error === "profile_unverified") {
      setProfileToken(null);
      profileUnverifiedListener?.();
    }
    throw new ApiClientError(res.status, apiErr.error, apiErr.message, apiErr);
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
  if (!(options.body instanceof FormData)) {
    headers["Content-Type"] = headers["Content-Type"] ?? "application/json";
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
    const apiErr = await parseApiError(res);
    throw new ApiClientError(res.status, apiErr.error, apiErr.message, apiErr);
  }

  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
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
