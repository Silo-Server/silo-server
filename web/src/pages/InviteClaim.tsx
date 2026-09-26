import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import { captureSessionIdentity, isSessionIdentityCurrent, getAccessToken } from "@/api/client";
import {
  acceptPublicInvitation,
  lookupPublicInvitation,
  type InvitationLookup,
} from "@/api/v2/publicInvitations";
import { sessionFromTokenPair } from "@/api/v2/account";
import { V2ProblemError } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";
import { Button } from "@/components/ui/button";
import { PasswordInput } from "@/components/PasswordInput";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { clearHouseholdSetupDone, setTourSuppressed } from "@/lib/onboarding";
import { buildInviteDeepLink, detectMobilePlatform } from "@/lib/appDeepLink";
import { Smartphone } from "lucide-react";
import { toast } from "sonner";

export default function InviteClaim() {
  const { token = "" } = useParams();
  return <ClaimForm key={token} token={token} />;
}

function ClaimForm({ token }: { token: string }) {
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  // Set alongside completeLogin: the moment auth state lands, this
  // component re-renders with a user — without the flag, the signed-in
  // redirect below would race our own navigate to /household-setup.
  const [accepted, setAccepted] = useState(false);
  const { user, loading, completeLogin } = useAuth();
  const navigate = useNavigate();

  const [lookup, setLookup] = useState<{
    data?: InvitationLookup;
    pending: boolean;
    unavailable?: boolean;
  }>({ pending: true });
  const [reload, setReload] = useState(0);
  const [recovery, setRecovery] = useState(false);
  const [createdUsername, setCreatedUsername] = useState<string | null>(null);
  const busy = useRef(false);
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => controller.abort();
  }, []);
  useEffect(() => {
    if (loading || user) return;
    const controller = new AbortController();
    const identity = captureSessionIdentity();
    lookupPublicInvitation(token, controller.signal)
      .then((data) => {
        if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
        setLookup({ data, pending: false });
        setRecovery(false);
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
        setLookup({
          pending: false,
          unavailable: error instanceof V2ProblemError && error.status === 404,
        });
      });
    return () => controller.abort();
  }, [token, reload, loading, user]);

  if (!loading && user && !accepted) return <Navigate to="/" replace />;
  if (loading || lookup.pending) {
    return (
      <div className="auth-shell">
        <div className="border-primary h-8 w-8 animate-spin rounded-full border-b-2" />
      </div>
    );
  }

  if (!lookup.data) {
    return (
      <div className="auth-shell">
        <AuthBackground />
        <Card className="auth-card glass panel-border w-full max-w-sm border-0">
          <CardHeader>
            <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
              {lookup.unavailable ? "Invitation unavailable" : "Could not load invitation"}
            </CardTitle>
            <CardDescription className="mt-2 text-sm leading-6">
              {lookup.unavailable
                ? "This link may have been used, revoked, or expired. Sign in if you already created your account, or ask for a fresh invitation."
                : "The server could not confirm this invitation. Try loading it again."}
            </CardDescription>
          </CardHeader>
          <CardContent>
            {!lookup.unavailable && (
              <Button className="mb-4 w-full" onClick={reloadLookup}>
                Reload invitation
              </Button>
            )}
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

  // On Android, offer to continue in the native app — the app registers
  // silo://invite and has the full claim flow. A user-tapped custom-scheme
  // link is the one context where silo:// works reliably; we never fire it
  // automatically (there is no installed-check, and a miss shows an OS
  // error). iOS joins once the Apple app registers the scheme.
  const platform = detectMobilePlatform(navigator.userAgent);
  const appLink =
    platform === "android" ? buildInviteDeepLink(window.location.origin, token) : null;

  function reloadLookup() {
    if (busy.current) return;
    setLookup({ pending: true });
    setReload((value) => value + 1);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (
      busy.current ||
      recovery ||
      createdUsername ||
      !invitation.acceptance_available ||
      getAccessToken()
    )
      return;
    if (password !== confirmPassword) {
      toast.error("Passwords do not match");
      return;
    }
    if ([...password].length < 8 || new TextEncoder().encode(password).length > 72) {
      toast.error("Use at least 8 characters and no more than 72 UTF-8 bytes.");
      return;
    }
    const controller = lifetime.current;
    if (!controller || controller.signal.aborted) return;
    const identity = captureSessionIdentity();
    busy.current = true;
    setSubmitting(true);
    try {
      const data = await acceptPublicInvitation(token, password, controller.signal);
      if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
      if (data.login_status === "sign_in_required") {
        setPassword("");
        setConfirmPassword("");
        setCreatedUsername(data.username);
        return;
      }
      if (!data.tokens) return; // Adapter rejects inconsistent outcomes.
      const session = sessionFromTokenPair(data.tokens);
      // No await between the expected-identity check and synchronous installation.
      if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
      completeLogin(session);
      setAccepted(true);
      setPassword("");
      setConfirmPassword("");
      clearHouseholdSetupDone();
      if (!invitation.show_tour) setTourSuppressed();
      navigate("/household-setup", { replace: true });
    } catch {
      if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
      setRecovery(true);
    } finally {
      busy.current = false;
      if (!controller.signal.aborted) setSubmitting(false);
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
          {appLink && (
            <div className="mb-6 space-y-3">
              <Button asChild size="lg" className="h-12 w-full text-base font-semibold">
                <a href={appLink}>
                  <Smartphone className="mr-2 h-5 w-5" /> Open in the Silo app
                </a>
              </Button>
              <p className="text-muted-foreground text-center text-xs">
                Nothing happens? The app isn&apos;t installed — just continue below.
              </p>
              <div className="flex items-center gap-3">
                <div className="border-border flex-1 border-t" />
                <span className="text-muted-foreground text-xs uppercase">
                  or set up in the browser
                </span>
                <div className="border-border flex-1 border-t" />
              </div>
            </div>
          )}
          {createdUsername ? (
            <div role="status" className="space-y-4">
              <p>
                Your account was created. Sign in as {createdUsername} with the password you chose.
              </p>
              <Button asChild className="w-full">
                <Link to="/login">Sign in</Link>
              </Button>
            </div>
          ) : (
            <>
              {!invitation.acceptance_available && (
                <p role="alert" className="mb-4">
                  This server cannot create the profile required by this invitation. Ask the
                  administrator for help.
                </p>
              )}
              {recovery && (
                <div role="alert" className="mb-4 space-y-3">
                  <p>
                    We could not confirm the result. Your account may have been created. Try signing
                    in, or reload this invitation before making another attempt.
                  </p>
                  <Button type="button" variant="outline" onClick={reloadLookup}>
                    Reload invitation
                  </Button>
                </div>
              )}
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
                    disabled={submitting}
                    autoComplete="new-password"
                    // On mobile, focusing here pops the keyboard over the
                    // open-in-app button — the primary action when it's shown.
                    autoFocus={!appLink}
                    required
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="invite-confirm-password">Confirm password</Label>
                  <PasswordInput
                    id="invite-confirm-password"
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    disabled={submitting}
                    autoComplete="new-password"
                    required
                  />
                  {confirmPassword && password !== confirmPassword && (
                    <p className="text-destructive text-xs">Passwords do not match</p>
                  )}
                </div>
                <Button
                  type="submit"
                  className="w-full"
                  disabled={submitting || recovery || !invitation.acceptance_available}
                >
                  {submitting ? "Creating account..." : "Create account"}
                </Button>
              </form>
            </>
          )}
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
