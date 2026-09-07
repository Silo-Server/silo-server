/**
 * Minimal fetch wrapper for the player module.
 * Uses PlayerConfig for API base URL and auth — never imports app-specific code.
 */

import type { PlayerConfig } from "./context/PlayerConfigContext";

interface PlayerErrorEnvelope {
  error?: string;
  message?: string;
}

export class PlayerFetchError extends Error {
  constructor(
    public status: number,
    message: string,
    public code?: string,
    public rawBody?: string,
  ) {
    super(message);
    this.name = "PlayerFetchError";
  }
}

/**
 * Performs an authenticated fetch against the configured API.
 * Returns the parsed JSON body for 2xx responses, undefined when the response
 * carries no body.
 * Throws PlayerFetchError for non-2xx responses.
 */
export async function playerFetch<T>(
  config: PlayerConfig,
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  };
  if (!(options.body instanceof FormData)) {
    headers["Content-Type"] = "application/json";
  }

  const authContext = config.getAuthContext?.();
  const token = config.getAccessToken();
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }

  const profileId = config.getProfileId();
  if (profileId) {
    headers["X-Profile-Id"] = profileId;
  }

  const profileToken = config.getProfileToken?.();
  if (profileToken) {
    headers["X-Profile-Token"] = profileToken;
  }

  headers["X-Silo-Device-Id"] = config.getDeviceId();

  let res = await fetch(`${config.apiBaseUrl}${path}`, {
    ...options,
    headers,
  });

  if (
    res.status === 401 &&
    config.refreshToken &&
    config.getAuthContext?.() === authContext &&
    !options.signal?.aborted
  ) {
    // A late 401 may arrive after another request already rotated the token.
    const alreadyRefreshed = config.getAccessToken() !== token && config.getAccessToken() !== null;
    const refreshed = alreadyRefreshed || (await config.refreshToken());
    const refreshedToken = config.getAccessToken();
    if (
      refreshed &&
      refreshedToken &&
      config.getAuthContext?.() === authContext &&
      !options.signal?.aborted
    ) {
      // Keep the original profile/PIN, device, body, and abort signal. Only
      // same-account token rotation may change authority during this retry.
      res = await fetch(`${config.apiBaseUrl}${path}`, {
        ...options,
        headers: { ...headers, Authorization: `Bearer ${refreshedToken}` },
      });
    }
  }

  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    let message = res.statusText || "Request failed";
    let code: string | undefined;

    if (text) {
      try {
        const parsed = JSON.parse(text) as PlayerErrorEnvelope;
        if (typeof parsed.message === "string" && parsed.message.trim().length > 0) {
          message = parsed.message.trim();
        } else if (text.trim().length > 0) {
          message = text.trim();
        }
        if (typeof parsed.error === "string" && parsed.error.trim().length > 0) {
          code = parsed.error.trim();
        }
      } catch {
        if (text.trim().length > 0) {
          message = text.trim();
        }
      }
    }

    throw new PlayerFetchError(res.status, message, code, text);
  }

  if (res.status === 204) {
    return undefined as T;
  }

  // Accepted operations may return either a JSON status object (for example
  // subtitle translation) or an empty acknowledgement (route diagnostics).
  // Inspect the payload rather than assigning body semantics to status 202.
  const text = await res.text();
  if (text.trim() === "") return undefined as T;
  return JSON.parse(text) as T;
}
