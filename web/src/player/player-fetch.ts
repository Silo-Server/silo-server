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
 * carries no body (204, and the 202 the route-event endpoint answers with).
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

  const res = await fetch(`${config.apiBaseUrl}${path}`, {
    ...options,
    headers,
  });

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

  // 204 never has a body; 202 is how the v3 route-event endpoint acknowledges
  // an accepted diagnostic, also with no body. Calling res.json() on either
  // throws on the empty payload.
  if (res.status === 204 || res.status === 202) {
    return undefined as T;
  }

  return res.json();
}
