import { refreshAccessToken } from "@/api/client";
import type { User } from "@/api/types";
import { decodeV2Response, V2_CLIENT_HEADERS, type V2Result } from "@/api/v2/request";

/**
 * The account as the app still models it. Login answers on v1 with numeric
 * ids, so the one account state keeps that shape until that operation moves;
 * the v2 id is the same account id rendered as a string.
 */
export function userFromAccount(account: V2Result<"GET /api/v2/account/me">): User {
  return {
    id: Number(account.id),
    username: account.username,
    email: account.email,
    role: account.role,
    permissions: account.permissions,
    download_allowed: account.download_allowed,
    impersonation: account.impersonation
      ? {
          active: account.impersonation.active,
          impersonator_user_id: Number(account.impersonation.impersonator_user_id),
          impersonator_username: account.impersonation.impersonator_username,
        }
      : null,
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
 * the user together with whichever tokens ended up working.
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
  let restoredAccessToken = accessToken;
  let restoredRefreshToken = refreshToken;

  const requestUser = (token: string) =>
    fetchImpl("/api/v2/account/me", {
      headers: {
        Accept: "application/json",
        ...V2_CLIENT_HEADERS,
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

  const account = await decodeV2Response("GET /api/v2/account/me", res);
  return {
    user: userFromAccount(account),
    accessToken: restoredAccessToken,
    refreshToken: restoredRefreshToken,
  };
}
