import { refreshAccessToken } from "@/api/client";
import { API_READ_TIMEOUT_MS } from "@/api/requestDeadline";
import type { LoginResponse, User } from "@/api/types";
import {
  decodeV2Response,
  V2_CLIENT_HEADERS,
  V2ProblemError,
  V2TransportError,
  withV2Deadline,
} from "@/api/v2/request";
import type { components } from "@/api/v2/schema";

/** The account document as every v2 credential and account response carries it. */
export type Account = components["schemas"]["Account"];

/** The token pair login, setup, signup, and device approval answer with. */
export type TokenPair = components["schemas"]["TokenPair"];

/**
 * The account as the app still models it: numeric ids, because the v1
 * responses that remain (admin impersonate) answer with
 * them; the v2 id is the same account id rendered as a string.
 */
export function userFromAccount(account: Account): User {
  return {
    id: Number(account.id),
    username: account.username,
    email: account.email,
    role: account.role,
    permissions: account.permissions,
    download_allowed: account.download_allowed,
    password_change_required: account.password_change_required,
    impersonation: account.impersonation
      ? {
          active: account.impersonation.active,
          impersonator_user_id: Number(account.impersonation.impersonator_user_id),
          impersonator_username: account.impersonation.impersonator_username,
        }
      : null,
  };
}

/**
 * Projects a v2 token pair onto the session shape the auth provider applies,
 * so a login, setup, signup, or device-approval answer and the v1 responses
 * that still produce one flow through the same state update.
 */
export function sessionFromTokenPair(pair: TokenPair): LoginResponse {
  return {
    access_token: pair.access_token,
    refresh_token: pair.refresh_token,
    expires_in: pair.expires_in,
    user: userFromAccount(pair.user),
  };
}

export interface RestoredUserSession {
  user: User;
  accessToken: string;
  refreshToken: string;
}

/**
 * Validates a stored token pair without touching the shared session: fetches
 * the account with the given access token, refreshes once on 401, and returns
 * the user together with whichever tokens ended up working. Each request has
 * the read deadline `v2()` gives, so a server that stops answering fails the
 * restore with `V2TimeoutError` instead of holding the app on its loading
 * screen.
 */
export async function restoreUserSession({
  accessToken,
  refreshToken,
  fetchImpl = fetch,
}: {
  accessToken: string;
  refreshToken: string;
  fetchImpl?: typeof fetch;
}): Promise<RestoredUserSession> {
  const readAccount = (token: string) =>
    withV2Deadline("GET /api/v2/account/me", API_READ_TIMEOUT_MS, null, async (signal) =>
      decodeV2Response(
        "GET /api/v2/account/me",
        await fetchImpl("/api/v2/account/me", {
          headers: {
            Accept: "application/json",
            ...V2_CLIENT_HEADERS,
            Authorization: `Bearer ${token}`,
          },
          signal,
        }),
      ),
    );

  try {
    const account = await readAccount(accessToken);
    return { user: userFromAccount(account), accessToken, refreshToken };
  } catch (error) {
    const unauthorized =
      (error instanceof V2ProblemError || error instanceof V2TransportError) &&
      error.status === 401;
    if (!unauthorized) throw error;
    const refreshed = await withV2Deadline(
      "POST /api/v2/auth/refresh",
      API_READ_TIMEOUT_MS,
      null,
      (signal) =>
        refreshAccessToken(refreshToken, (input, init) => fetchImpl(input, { ...init, signal })),
    );
    if (!refreshed) throw error;
    const account = await readAccount(refreshed.access_token);
    return {
      user: userFromAccount(account),
      accessToken: refreshed.access_token,
      refreshToken: refreshed.refresh_token,
    };
  }
}
