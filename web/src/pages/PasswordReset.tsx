import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link, useParams } from "react-router";
import { captureSessionIdentity, getAccessToken, isSessionIdentityCurrent } from "@/api/client";
import {
  completePasswordReset,
  lookupPasswordReset,
  type PasswordResetLookup,
} from "@/api/v2/publicPasswordResets";
import { sessionFromTokenPair } from "@/api/v2/account";
import { V2ProblemError } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { usePostSignInNavigation } from "@/hooks/usePostSignInNavigation";
import { usePasswordResetAvailable } from "@/hooks/queries/passwordReset";
import { formatDateTime } from "@/lib/datetime";
import { newPasswordProblem, passwordErrorMessage } from "@/lib/newPassword";
import { Button } from "@/components/ui/button";
import { PasswordInput } from "@/components/PasswordInput";
import { Label } from "@/components/ui/label";
import { AuthCard, SignInHint } from "@/components/auth/AuthCard";
import { useT } from "@/i18n";

export default function PasswordReset() {
  const { token = "" } = useParams();
  return <ResetForm key={token} token={token} />;
}

function ResetForm({ token }: { token: string }) {
  const { user: settledUser, pendingPasswordChange, loading, logout, completeLogin } = useAuth();
  // A session holding a temporary password is signed in too.
  const user = settledUser ?? pendingPasswordChange;
  const continueSignIn = usePostSignInNavigation(null);
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  // Set alongside completeLogin: the signed-in notice below must not replace
  // this screen while it navigates on.
  const [completed, setCompleted] = useState(false);
  const [signInAs, setSignInAs] = useState<string | null>(null);
  const [recovery, setRecovery] = useState(false);
  const [lookup, setLookup] = useState<{
    data?: PasswordResetLookup;
    pending: boolean;
    unavailable?: boolean;
  }>({ pending: true });
  const [reload, setReload] = useState(0);
  // Offering a new link matters only once this one proved unusable.
  const { available: canRequestLink } = usePasswordResetAvailable(lookup.unavailable === true);
  const busy = useRef(false);
  const lifetime = useRef<AbortController | null>(null);
  const t = useT();
  useDocumentTitle(t("web.auth.passwordReset.documentTitle"));

  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => controller.abort();
  }, []);
  useEffect(() => {
    if (loading || user) return;
    const controller = new AbortController();
    const identity = captureSessionIdentity();
    lookupPasswordReset(token, controller.signal)
      .then((data) => {
        if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
        setLookup({ data, pending: false });
        setRecovery(false);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
        setLookup({
          pending: false,
          unavailable: err instanceof V2ProblemError && err.status === 404,
        });
      });
    return () => controller.abort();
  }, [token, reload, loading, user]);

  function reloadLookup() {
    if (busy.current) return;
    setLookup({ pending: true });
    setReload((value) => value + 1);
  }

  // Someone is signed in on this browser: completing the reset would replace
  // their session with the reset account's, so they sign out first.
  if (!loading && user && !completed) {
    return (
      <AuthCard title={t("web.auth.passwordRules.resetPasswordTitle")}>
        <p className="mb-4 text-sm">
          {t("web.auth.passwordReset.signedIn", { username: user.username })}
        </p>
        <Button className="w-full" onClick={logout}>
          {t("common.signOut")}
        </Button>
      </AuthCard>
    );
  }
  if (loading || lookup.pending) {
    return (
      <div className="auth-shell">
        <div className="border-primary h-8 w-8 animate-spin rounded-full border-b-2" />
      </div>
    );
  }
  if (signInAs) {
    return (
      <AuthCard title={t("web.auth.passwordRules.passwordChanged")}>
        <p role="status" className="mb-4 text-sm">
          {t("web.auth.passwordReset.signInAs", { username: signInAs })}
        </p>
        <Button asChild className="w-full">
          <Link to="/login">{t("web.auth.signIn")}</Link>
        </Button>
      </AuthCard>
    );
  }
  if (!lookup.data) {
    return (
      <AuthCard
        title={
          lookup.unavailable
            ? t("web.auth.passwordReset.unavailableTitle")
            : t("web.auth.passwordReset.loadFailedTitle")
        }
        description={
          lookup.unavailable
            ? canRequestLink
              ? t("web.auth.passwordReset.unavailableRequestNew")
              : t("web.auth.passwordReset.unavailableAskAdmin")
            : t("web.auth.passwordReset.loadFailed")
        }
      >
        {!lookup.unavailable && (
          <Button className="mb-4 w-full" onClick={reloadLookup}>
            {t("web.auth.passwordReset.reloadLink")}
          </Button>
        )}
        {lookup.unavailable && canRequestLink && (
          <Button asChild className="mb-4 w-full">
            <Link to="/forgot-password">{t("web.auth.passwordReset.requestNewLink")}</Link>
          </Button>
        )}
        <SignInHint />
      </AuthCard>
    );
  }
  const reset = lookup.data;

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (busy.current || recovery || getAccessToken()) return;
    const problem = newPasswordProblem(password, confirmation);
    if (problem) {
      setError(problem);
      return;
    }
    const controller = lifetime.current;
    if (!controller || controller.signal.aborted) return;
    const identity = captureSessionIdentity();
    busy.current = true;
    setSubmitting(true);
    setError("");
    try {
      const result = await completePasswordReset(token, password, controller.signal);
      if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
      setPassword("");
      setConfirmation("");
      if (result.login_status === "sign_in_required" || !result.tokens) {
        setSignInAs(result.username);
        return;
      }
      const session = sessionFromTokenPair(result.tokens);
      completeLogin(session);
      setCompleted(true);
      await continueSignIn(session.user);
    } catch (err) {
      if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
      // A rejected password leaves the link unspent; anything else may have
      // committed, so the link is reloaded before another attempt.
      if (err instanceof V2ProblemError && err.problemType === "validation_failed") {
        setError(passwordErrorMessage(err, t("web.auth.passwordReset.setFailed")));
      } else if (err instanceof V2ProblemError && err.status === 404) {
        setLookup({ pending: false, unavailable: true });
      } else {
        setRecovery(true);
      }
    } finally {
      busy.current = false;
      if (!controller.signal.aborted) setSubmitting(false);
    }
  }

  return (
    <AuthCard
      eyebrow={reset.server_name}
      title={t("web.auth.passwordRules.chooseNewPassword")}
      description={t("web.auth.passwordReset.description", {
        username: reset.username,
        expires: formatDateTime(reset.expires_at),
      })}
    >
      {error && (
        <p role="alert" className="text-destructive mb-4 text-sm">
          {error}
        </p>
      )}
      {recovery && (
        <div role="alert" className="mb-4 space-y-3 text-sm">
          <p>{t("web.auth.passwordReset.recovery")}</p>
          <Button type="button" variant="outline" onClick={reloadLookup}>
            {t("web.auth.passwordReset.reloadLink")}
          </Button>
        </div>
      )}
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="reset-password">{t("web.auth.passwordRules.newPassword")}</Label>
          <p className="text-muted-foreground text-xs">{t("web.auth.passwordRules.minLength")}</p>
          <PasswordInput
            id="reset-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={submitting}
            autoComplete="new-password"
            autoFocus
            required
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="reset-confirm-password">
            {t("web.auth.passwordRules.confirmNewPassword")}
          </Label>
          <PasswordInput
            id="reset-confirm-password"
            value={confirmation}
            onChange={(e) => setConfirmation(e.target.value)}
            disabled={submitting}
            autoComplete="new-password"
            required
          />
        </div>
        <Button type="submit" className="w-full" disabled={submitting || recovery}>
          {submitting ? t("web.ui.saving") : t("web.auth.passwordRules.savePassword")}
        </Button>
      </form>
      <SignInHint />
    </AuthCard>
  );
}
