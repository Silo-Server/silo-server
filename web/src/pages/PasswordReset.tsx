import { useEffect, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
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
import { formatDateTime } from "@/lib/datetime";
import { newPasswordProblem, passwordErrorMessage } from "@/lib/newPassword";
import { Button } from "@/components/ui/button";
import { PasswordInput } from "@/components/PasswordInput";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { AuthBackground } from "@/components/auth/AuthBackground";

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
  const busy = useRef(false);
  const lifetime = useRef<AbortController | null>(null);
  useDocumentTitle("Reset Password");

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
      <ResetCard title="Reset password">
        <p className="mb-4 text-sm">
          You&apos;re signed in as {user.username}. Sign out to use this reset link.
        </p>
        <Button className="w-full" onClick={logout}>
          Sign out
        </Button>
      </ResetCard>
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
      <ResetCard title="Password changed">
        <p role="status" className="mb-4 text-sm">
          Sign in as {signInAs} with your new password.
        </p>
        <Button asChild className="w-full">
          <Link to="/login">Sign in</Link>
        </Button>
      </ResetCard>
    );
  }
  if (!lookup.data) {
    return (
      <ResetCard
        title={lookup.unavailable ? "Link unavailable" : "Could not load link"}
        description={
          lookup.unavailable
            ? "This link was already used, replaced by a newer one, or expired. Ask your admin for a new link."
            : "The server could not confirm this link. Try loading it again."
        }
      >
        {!lookup.unavailable && (
          <Button className="mb-4 w-full" onClick={reloadLookup}>
            Reload link
          </Button>
        )}
        <SignInHint />
      </ResetCard>
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
        setError(passwordErrorMessage(err, "Could not set the password."));
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
    <ResetCard
      eyebrow={reset.server_name}
      title="Choose a new password"
      description={`For ${reset.username}. The link expires ${formatDateTime(reset.expires_at)}. Saving signs this account out on every device.`}
    >
      {error && (
        <p role="alert" className="text-destructive mb-4 text-sm">
          {error}
        </p>
      )}
      {recovery && (
        <div role="alert" className="mb-4 space-y-3 text-sm">
          <p>
            We could not confirm the result. Your password may have changed. Try signing in with the
            new password, or reload this link before trying again.
          </p>
          <Button type="button" variant="outline" onClick={reloadLookup}>
            Reload link
          </Button>
        </div>
      )}
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="reset-password">New password</Label>
          <p className="text-muted-foreground text-xs">At least 8 characters</p>
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
          <Label htmlFor="reset-confirm-password">Confirm new password</Label>
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
          {submitting ? "Saving..." : "Save password"}
        </Button>
      </form>
      <SignInHint />
    </ResetCard>
  );
}

function ResetCard({
  eyebrow,
  title,
  description,
  children,
}: {
  eyebrow?: string;
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <div className="auth-shell">
      <AuthBackground />
      <Card className="auth-card glass panel-border w-full max-w-sm border-0">
        <CardHeader>
          {eyebrow && (
            <p className="text-muted-foreground font-mono text-[11px] font-semibold tracking-[0.1em] uppercase">
              {eyebrow}
            </p>
          )}
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">{title}</CardTitle>
          {description && (
            <CardDescription className="mt-2 text-sm leading-6">{description}</CardDescription>
          )}
        </CardHeader>
        <CardContent>{children}</CardContent>
      </Card>
    </div>
  );
}

function SignInHint() {
  return (
    <p className="text-muted-foreground mt-4 text-center text-sm">
      Remembered it?{" "}
      <Link to="/login" className="text-foreground underline hover:no-underline">
        Sign in
      </Link>
    </p>
  );
}
