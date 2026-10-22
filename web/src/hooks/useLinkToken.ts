import { useEffect } from "react";
import { useLocation, useNavigate, useParams } from "react-router";

/**
 * The one-time token of a password reset or invitation link. Links carry it
 * in the fragment (`/reset-password#<token>`), which browsers never send to
 * the server or in a Referer; links issued by older servers carry it in the
 * path (`/reset-password/<token>`). Either way the current history entry is
 * replaced with `basePath` straight away, so the token leaves the address bar
 * and later requests' Referer, and moves into the entry's state, where a
 * reload still finds it. The browser's own history may still list the address
 * the link opened.
 *
 * Returns null while that replacement is pending, and "" when the page was
 * opened without a token.
 */
export function useLinkToken(basePath: string): string | null {
  const { token: pathToken } = useParams();
  const { hash, state } = useLocation();
  const navigate = useNavigate();
  const urlToken = pathToken || hash.slice(1);

  useEffect(() => {
    if (urlToken) {
      navigate(basePath, { replace: true, state: { linkToken: urlToken } });
    }
  }, [urlToken, basePath, navigate]);

  if (urlToken) return null;
  const stateToken = (state as { linkToken?: unknown } | null)?.linkToken;
  return typeof stateToken === "string" ? stateToken : "";
}
