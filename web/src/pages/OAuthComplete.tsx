import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { setAccessToken, setRefreshToken } from "@/api/client";
import { userFromAccount } from "@/api/v2/account";
import { v2, V2ProblemError, type V2Result } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";
import { sanitizeAuthRedirect } from "@/lib/authRedirect";

type OAuthCompletion = V2Result<"POST /api/v2/auth/oauth/complete">;

async function completeOAuthCode(code: string): Promise<OAuthCompletion> {
  try {
    return await v2("POST /api/v2/auth/oauth/complete", { body: { code } });
  } catch (err) {
    if (err instanceof V2ProblemError) {
      throw new Error("Sign-in response expired. Please try again.");
    }
    throw err;
  }
}

export default function OAuthComplete() {
  const navigate = useNavigate();
  const { completeLogin } = useAuth();
  const [error, setError] = useState<string | null>(() =>
    new URLSearchParams(window.location.search).get("code")
      ? null
      : "Sign-in response missing completion code. Please try again.",
  );

  useEffect(() => {
    const code = new URLSearchParams(window.location.search).get("code");
    if (!code) {
      return;
    }
    window.history.replaceState(null, "", window.location.pathname);

    let cancelled = false;
    (async () => {
      try {
        const tokens = await completeOAuthCode(code);
        if (cancelled) return;
        const next = sanitizeAuthRedirect(tokens.next) || "/";
        setAccessToken(tokens.access_token);
        setRefreshToken(tokens.refresh_token);
        const user = userFromAccount(await v2("GET /api/v2/account/me"));
        if (cancelled) return;
        completeLogin({
          access_token: tokens.access_token,
          refresh_token: tokens.refresh_token,
          expires_in: tokens.expires_in,
          user,
        });
        navigate(next, { replace: true });
      } catch (err) {
        if (!cancelled) {
          setAccessToken(null);
          setRefreshToken(null);
          setError(err instanceof Error ? err.message : "Failed to complete sign-in");
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [completeLogin, navigate]);

  if (error) {
    return (
      <div className="auth-shell">
        <div className="border-destructive/30 bg-destructive/10 text-destructive max-w-md rounded-md border p-4 text-sm">
          {error}
        </div>
      </div>
    );
  }

  return (
    <div className="auth-shell">
      <div className="border-primary h-8 w-8 animate-spin rounded-full border-b-2" />
    </div>
  );
}
