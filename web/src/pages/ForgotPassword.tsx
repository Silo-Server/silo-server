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
  useDocumentTitle("Forgot Password");

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
          aria-label="Loading"
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
      <AuthCard eyebrow={serverName} title="Check your email">
        <p role="status" className="mb-3 text-sm">
          If {sentFor} matches an account with an email address, we sent that address a link to
          choose a new password.
        </p>
        <p className="text-muted-foreground mb-4 text-sm">
          Nothing after a few minutes? Check your spam folder, then try again.
        </p>
        <Button asChild className="w-full">
          <Link to="/login">Back to sign in</Link>
        </Button>
      </AuthCard>
    );
  }
  if (capability.failed) {
    return (
      <AuthCard
        eyebrow={serverName}
        title="Reset password"
        description="The server could not be reached. Try again."
      >
        <Button className="w-full" onClick={capability.retry}>
          Try again
        </Button>
        <SignInHint />
      </AuthCard>
    );
  }
  if (!capability.available || refused) {
    return (
      <AuthCard
        eyebrow={serverName}
        title="Reset password"
        description="This server doesn't offer password resets from the sign-in page. Ask your admin to reset your password."
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
        setError("Too many requests from this network. Wait a minute, then try again.");
      } else {
        setError("Could not send the request. Try again.");
      }
    } finally {
      if (!controller.signal.aborted) setSubmitting(false);
    }
  }

  return (
    <AuthCard
      eyebrow={serverName}
      title="Reset password"
      description="Enter your username or email. If it matches an account, we'll email that account a link to choose a new password."
    >
      {error && (
        <p role="alert" className="text-destructive mb-4 text-sm">
          {error}
        </p>
      )}
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="forgot-login">Username or email</Label>
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
          {submitting ? "Sending..." : "Send reset link"}
        </Button>
      </form>
      <SignInHint />
    </AuthCard>
  );
}
