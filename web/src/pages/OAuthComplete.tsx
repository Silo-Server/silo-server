import { useEffect, useRef } from "react";
import { useNavigate } from "react-router";
import { setAccessToken, setRefreshToken } from "@/api/client";
import { userFromAccount } from "@/api/v2/account";
import { v2, V2ProblemError } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";
import { sanitizeAuthRedirect } from "@/lib/authRedirect";

/**
 * A failed completion goes back to the login page with a reason= code the
 * page explains. With error= set the page never redirects to the provider
 * on its own, so a completion that keeps failing cannot loop.
 */
function loginFailureHref(reason: string) {
  return `/login?error=oauth_failed&reason=${reason}`;
}

export default function OAuthComplete() {
  const navigate = useNavigate();
  const { completeLogin } = useAuth();
  // The code is single use and leaves the address bar once read, so the
  // completion runs once; only leaving the page abandons it.
  const started = useRef(false);
  const unmounted = useRef(false);
  useEffect(
    () => () => {
      unmounted.current = true;
    },
    [],
  );

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    const code = new URLSearchParams(window.location.search).get("code");
    if (!code) {
      // Reached without the server's redirect, e.g. reloaded after use.
      navigate(loginFailureHref("state_invalid"), { replace: true });
      return;
    }
    window.history.replaceState(null, "", window.location.pathname);

    void (async () => {
      // The completion code lives 60 seconds and works once; the server
      // refusing it means it expired or was already used.
      let redeemed = false;
      try {
        const tokens = await v2("POST /api/v2/auth/oauth/complete", { body: { code } });
        redeemed = true;
        if (unmounted.current) return;
        const next = sanitizeAuthRedirect(tokens.next) || "/";
        setAccessToken(tokens.access_token);
        setRefreshToken(tokens.refresh_token);
        const user = userFromAccount(await v2("GET /api/v2/account/me"));
        if (unmounted.current) return;
        completeLogin({
          access_token: tokens.access_token,
          refresh_token: tokens.refresh_token,
          expires_in: tokens.expires_in,
          user,
        });
        navigate(next, { replace: true });
      } catch (err) {
        if (!unmounted.current) {
          setAccessToken(null);
          setRefreshToken(null);
          const refused = !redeemed && err instanceof V2ProblemError;
          navigate(loginFailureHref(refused ? "session_expired" : "login_failed"), {
            replace: true,
          });
        }
      }
    })();
  }, [completeLogin, navigate]);

  return (
    <div className="auth-shell">
      <div
        className="border-primary h-8 w-8 animate-spin rounded-full border-b-2"
        role="status"
        aria-label="Signing in"
      />
    </div>
  );
}
