import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link, Navigate, useSearchParams } from "react-router";
import { requestPasswordReset } from "@/api/v2/publicPasswordResets";
import { V2ProblemError } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useServerBranding } from "@/hooks/useServerBranding";
import { usePasswordResetAvailable } from "@/hooks/queries/passwordReset";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { AuthCard, SignInHint } from "@/components/auth/AuthCard";
import { useT } from "@/i18n";

export default function ForgotPassword() {
  const [searchParams] = useSearchParams();
  const { user, pendingPasswordChange, loading } = useAuth();
  const { serverName } = useServerBranding();
  const capability = usePasswordResetAvailable();
  const [login, setLogin] = useState(searchParams.get("login") ?? "");
  const [sentFor, setSentFor] = useState<string | null>(null);
  // The server refused the request itself: the feature was turned off or lost
  // its email setup after this page loaded.
  const [refused, setRefused] = useState(false);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const lifetime = useRef<AbortController | null>(null);
  const t = useT();
  useDocumentTitle(t("web.auth.forgotPassword.documentTitle"));

  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => controller.abort();
  }, []);

  if (loading || (capability.pending && sentFor === null)) {
    return (
      <div className="auth-shell">
        <div
          className="border-primary h-8 w-8 animate-spin rounded-full border-b-2"
          role="status"
          aria-label={t("common.loading")}
        />
      </div>
    );
  }
  // A signed-in account changes its password from its own settings.
  if (user || pendingPasswordChange) {
    return <Navigate to="/" replace />;
  }
  if (sentFor !== null) {
    return (
      <AuthCard eyebrow={serverName} title={t("web.auth.forgotPassword.checkEmailTitle")}>
        <p role="status" className="mb-3 text-sm">
          {t("web.auth.forgotPassword.sent", { login: sentFor })}
        </p>
        <p className="text-muted-foreground mb-4 text-sm">
          {t("web.auth.forgotPassword.spamHint")}
        </p>
        <Button asChild className="w-full">
          <Link to="/login">{t("web.auth.forgotPassword.backToSignIn")}</Link>
        </Button>
      </AuthCard>
    );
  }
  if (capability.failed) {
    return (
      <AuthCard
        eyebrow={serverName}
        title={t("web.auth.passwordRules.resetPasswordTitle")}
        description={t("web.auth.forgotPassword.unreachable")}
      >
        <Button className="w-full" onClick={capability.retry}>
          {t("common.tryAgain")}
        </Button>
        <SignInHint />
      </AuthCard>
    );
  }
  if (!capability.available || refused) {
    return (
      <AuthCard
        eyebrow={serverName}
        title={t("web.auth.passwordRules.resetPasswordTitle")}
        description={t("web.auth.forgotPassword.unavailable")}
      >
        <SignInHint />
      </AuthCard>
    );
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const trimmed = login.trim();
    const controller = lifetime.current;
    if (!trimmed || submitting || !controller || controller.signal.aborted) return;
    setSubmitting(true);
    setError("");
    try {
      await requestPasswordReset(trimmed, controller.signal);
      if (controller.signal.aborted) return;
      setSentFor(trimmed);
    } catch (err) {
      if (controller.signal.aborted) return;
      if (err instanceof V2ProblemError && err.status === 409) {
        setRefused(true);
      } else if (err instanceof V2ProblemError && err.status === 429) {
        setError(t("web.auth.forgotPassword.rateLimited"));
      } else {
        setError(t("web.auth.forgotPassword.sendFailed"));
      }
    } finally {
      if (!controller.signal.aborted) setSubmitting(false);
    }
  }

  return (
    <AuthCard
      eyebrow={serverName}
      title={t("web.auth.passwordRules.resetPasswordTitle")}
      description={t("web.auth.forgotPassword.description")}
    >
      {error && (
        <p role="alert" className="text-destructive mb-4 text-sm">
          {error}
        </p>
      )}
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="forgot-login">{t("web.auth.forgotPassword.loginLabel")}</Label>
          <Input
            id="forgot-login"
            value={login}
            onChange={(e) => setLogin(e.target.value)}
            disabled={submitting}
            autoComplete="username"
            autoFocus
            required
          />
        </div>
        <Button type="submit" className="w-full" disabled={submitting}>
          {submitting ? t("web.auth.forgotPassword.sending") : t("web.auth.forgotPassword.send")}
        </Button>
      </form>
      <SignInHint />
    </AuthCard>
  );
}
