import { V2_CLIENT_HEADERS, decodeV2Response } from "@/api/v2/request";

/**
 * Ends the Silo login session with the bearer captured before the web client
 * cleared its state, then sends the browser to the sign-in provider's
 * end-session URL when the provider offers one and its operator turned it on
 * (docs/architecture/external-sign-in.md, "Provider logout").
 *
 * The URL is asked for first: the server answers it for the session's own
 * identity, which the logout ends. The lookup is bounded so a slow provider
 * cannot hold up the Silo logout. A server without the operation, a failure,
 * a timeout or an empty answer leaves only the Silo logout. withProvider false skips
 * the provider entirely, for "Switch account", which must leave the
 * provider's own session alone.
 */
/** How long the logout waits for the provider end-session URL. */
export const PROVIDER_LOGOUT_LOOKUP_TIMEOUT_MS = 2500;

export async function endSessionWithProvider(
  accessToken: string,
  navigate: (url: string) => void = (url) => window.location.assign(url),
  { withProvider = true }: { withProvider?: boolean } = {},
): Promise<void> {
  const headers = {
    ...V2_CLIENT_HEADERS,
    Accept: "application/json",
    Authorization: `Bearer ${accessToken}`,
  };
  let endSessionUrl = "";
  if (withProvider) {
    try {
      const res = await fetch("/api/v2/auth/provider-logout", {
        headers,
        signal: AbortSignal.timeout(PROVIDER_LOGOUT_LOOKUP_TIMEOUT_MS),
      });
      const answer = await decodeV2Response("GET /api/v2/auth/provider-logout", res);
      endSessionUrl = answer.end_session_url ?? "";
    } catch {
      // Older server or no provider: the Silo logout below is all there is.
    }
  }
  await fetch("/api/v2/auth/logout", { method: "POST", headers, keepalive: true }).catch(
    () => undefined,
  );
  if (/^https?:\/\//i.test(endSessionUrl)) {
    navigate(endSessionUrl);
  }
}
