import { useState } from "react";
import type { FormEvent } from "react";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { InvitationLookupResponse, LoginResponse } from "@/api/types";
import { useAuth } from "@/hooks/useAuth";
import { Button } from "@/components/ui/button";
import { PasswordInput } from "@/components/PasswordInput";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { clearHouseholdSetupDone, setTourSuppressed } from "@/lib/onboarding";
import { toast } from "sonner";

/**
 * Public claim screen for an emailed invitation: /invite/:token.
 * Everything except the password was decided when the invite was sent, so
 * this screen asks for exactly one thing. On success the accept response is
 * a normal login payload — the user lands signed in, never at /login.
 */
export default function InviteClaim() {
  const { token = "" } = useParams();
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  // Set right before completeLogin: the moment auth state lands, this
  // component re-renders with a user — without the flag, the signed-in
  // redirect below would race our own navigate to /household-setup.
  const [accepted, setAccepted] = useState(false);
  const { user, loading, completeLogin } = useAuth();
  const navigate = useNavigate();

  const lookup = useQuery({
    queryKey: ["invitation", token],
    queryFn: () => api<InvitationLookupResponse>(`/invitations/${token}`),
    enabled: token !== "",
    retry: false,
    staleTime: Infinity,
  });

  if (loading || lookup.isPending) {
    return (
      <div className="auth-shell">
        <div className="border-primary h-8 w-8 animate-spin rounded-full border-b-2" />
      </div>
    );
  }

  // Already signed in — an invite link can't act on this session. Skipped
  // when this very screen just created the session (accepted).
  if (user && !accepted) {
    return <Navigate to="/" replace />;
  }

  if (lookup.isError || !lookup.data) {
    return (
      <div className="auth-shell">
        <AuthBackground />
        <Card className="auth-card glass panel-border w-full max-w-sm border-0">
          <CardHeader>
            <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
              Invitation expired
            </CardTitle>
            <CardDescription className="mt-2 text-sm leading-6">
              This invite link is no longer valid — it may have been used already, revoked, or
              simply expired. Ask whoever invited you to send a fresh one.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-muted-foreground text-center text-sm">
              Already have an account?{" "}
              <Link to="/login" className="text-foreground underline hover:no-underline">
                Sign in
              </Link>
            </p>
          </CardContent>
        </Card>
      </div>
    );
  }

  const invitation = lookup.data;

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (password !== confirmPassword) {
      toast.error("Passwords do not match");
      return;
    }
    setSubmitting(true);
    try {
      const data = await api<LoginResponse>(`/invitations/${token}/accept`, {
        method: "POST",
        body: JSON.stringify({ password }),
      });
      setAccepted(true);
      // A fresh account starts household setup fresh, even if a previous
      // invitee finished theirs on this browser.
      clearHouseholdSetupDone();
      completeLogin(data);
      if (!invitation.show_tour) {
        // The onboarding gate honors this hint by recording a server-side
        // skip once a profile is active, then clears it.
        setTourSuppressed();
      }
      navigate("/household-setup", { replace: true });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not create your account");
      setSubmitting(false);
    }
  }

  return (
    <div className="auth-shell">
      <AuthBackground />
      <Card className="auth-card glass panel-border w-full max-w-sm border-0">
        <CardHeader>
          {invitation.inviter_name && (
            <p className="text-muted-foreground font-mono text-[11px] font-semibold tracking-[0.1em] uppercase">
              Invited by {invitation.inviter_name}
            </p>
          )}
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
            Welcome to {invitation.server_name}
          </CardTitle>
          <CardDescription className="mt-2 text-sm leading-6">
            Choose a password and you&apos;re in. You&apos;ll sign in with your email address.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="invite-email">Email</Label>
              <Input id="invite-email" value={invitation.email} readOnly disabled />
            </div>
            <div className="space-y-2">
              <Label htmlFor="invite-password">Password</Label>
              <p className="text-muted-foreground text-xs">At least 8 characters</p>
              <PasswordInput
                id="invite-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
                autoFocus
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="invite-confirm-password">Confirm password</Label>
              <PasswordInput
                id="invite-confirm-password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                autoComplete="new-password"
                required
              />
              {confirmPassword && password !== confirmPassword && (
                <p className="text-destructive text-xs">Passwords do not match</p>
              )}
            </div>
            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting ? "Creating account..." : "Create account"}
            </Button>
          </form>
          <p className="text-muted-foreground mt-4 text-center text-sm">
            Already set this up?{" "}
            <Link to="/login" className="text-foreground underline hover:no-underline">
              Sign in
            </Link>
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
