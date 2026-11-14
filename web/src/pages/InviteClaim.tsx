import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link, Navigate, useNavigate } from "react-router";
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
import { AuthLanguagePicker } from "@/components/auth/AuthLanguagePicker";
import { clearHouseholdSetupDone, setTourSuppressed } from "@/lib/onboarding";
import { buildInviteDeepLink, detectMobilePlatform } from "@/lib/appDeepLink";
import { Smartphone } from "lucide-react";
import { toast } from "sonner";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import { useLinkToken } from "@/hooks/useLinkToken";
import { useT } from "@/i18n";

export default function InviteClaim() {
  const token = useLinkToken();
  return <ClaimForm key={token} token={token} />;
}

function ClaimForm({ token }: { token: string }) {
  const [email, setEmail] = useState("");
  const [emailError, setEmailError] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  // Set alongside completeLogin: the moment auth state lands, this
  // component re-renders with a user — without the flag, the signed-in
  // redirect below would race our own navigate to /household-setup.
  const [accepted, setAccepted] = useState(false);
  const { user, loading, completeLogin } = useAuth();
  const navigate = useNavigate();
  const t = useT();

  const [lookup, setLookup] = useState<{
    data?: InvitationLookup;
    pending: boolean;
    unavailable?: boolean;
  }>(
    // Opened without its token, as when something on the way dropped the
    // link's fragment, there is nothing to look up: the invitation is
    // unavailable, and a fresh one is the remedy.
    token ? { pending: true } : { pending: false, unavailable: true },
  );
  const [reload, setReload] = useState(0);
  const [recovery, setRecovery] = useState(false);
  // The server refused the acceptance because password sign-in is off; the
  // invitation stays unspent, and no account was created.
  const [localLoginDisabled, setLocalLoginDisabled] = useState(false);
  const [createdUsername, setCreatedUsername] = useState<string | null>(null);
  const busy = useRef(false);
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => controller.abort();
  }, []);
  useEffect(() => {
    if (loading || user || !token) return;
    const controller = new AbortController();
    const identity = captureSessionIdentity();
    lookupPublicInvitation(token, controller.signal)
      .then((data) => {
        if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
        setLookup({ data, pending: false });
        setRecovery(false);
        setLocalLoginDisabled(false);
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
        <AuthLanguagePicker />
        <Card className="auth-card glass panel-border w-full max-w-sm border-0">
          <CardHeader>
            <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
              {lookup.unavailable
                ? t("web.auth.invite.unavailableTitle")
                : t("web.auth.invite.loadFailedTitle")}
            </CardTitle>
            <CardDescription className="mt-2 text-sm leading-6">
              {lookup.unavailable
                ? t("web.auth.invite.unavailable")
                : t("web.auth.invite.loadFailed")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            {!lookup.unavailable && (
              <Button className="mb-4 w-full" onClick={reloadLookup}>
                {t("web.auth.invite.reload")}
              </Button>
            )}
            <p className="text-muted-foreground text-center text-sm">
              {t("web.auth.passwordRules.alreadyHaveAccount", {
                link: (chunks) => (
                  <Link to="/login" className="text-foreground underline hover:no-underline">
                    {chunks}
                  </Link>
                ),
              })}
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
  // error). iOS joins once the Apple app registers the scheme. A link
  // invitation stays in the browser: released app builds cannot ask for the
  // email address it needs, and the page cannot tell which build is installed.
  const platform = detectMobilePlatform(navigator.userAgent);
  const appLink =
    platform === "android" && !invitation.email_required
      ? buildInviteDeepLink(window.location.origin, token)
      : null;

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
      localLoginDisabled ||
      createdUsername ||
      !invitation.acceptance_available ||
      getAccessToken()
    )
      return;
    // A link invitation has no address yet: the invitee enters theirs.
    const enteredEmail = invitation.email_required ? email.trim() : undefined;
    if (enteredEmail !== undefined && !isValidEmail(enteredEmail)) {
      setEmailError(invalidEmailMessage());
      return;
    }
    if (password !== confirmPassword) {
      toast.error(t("web.auth.passwordRules.mismatch"));
      return;
    }
    if ([...password].length < 8 || new TextEncoder().encode(password).length > 72) {
      toast.error(t("web.auth.invite.passwordLength"));
      return;
    }
    const controller = lifetime.current;
    if (!controller || controller.signal.aborted) return;
    const identity = captureSessionIdentity();
    busy.current = true;
    setSubmitting(true);
    try {
      const data = await acceptPublicInvitation(token, password, controller.signal, enteredEmail);
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
    } catch (error: unknown) {
      if (controller.signal.aborted || !isSessionIdentityCurrent(identity)) return;
      if (error instanceof V2ProblemError && error.problemType === "local_login_disabled") {
        setLocalLoginDisabled(true);
        return;
      }
      // A refused address is a definite answer: no account was created and
      // the link still works, so the invitee can correct it and retry.
      if (enteredEmail !== undefined && error instanceof V2ProblemError) {
        if (error.problemType === "conflict") {
          setEmailError(t("web.auth.invite.emailTaken"));
          return;
        }
        if (
          error.problemType === "validation_failed" &&
          error.problem.errors?.some((e) => e.location === "body.email")
        ) {
          setEmailError(invalidEmailMessage());
          return;
        }
      }
      setRecovery(true);
    } finally {
      busy.current = false;
      if (!controller.signal.aborted) setSubmitting(false);
    }
  }

  return (
    <div className="auth-shell">
      <AuthBackground />
      <AuthLanguagePicker />
      <Card className="auth-card glass panel-border w-full max-w-sm border-0">
        <CardHeader>
          {invitation.inviter_name && (
            <p className="text-muted-foreground font-mono text-[11px] font-semibold tracking-[0.1em] uppercase">
              {t("web.auth.invite.invitedBy", { inviter: invitation.inviter_name })}
            </p>
          )}
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">
            {t("web.auth.invite.title", { server: invitation.server_name })}
          </CardTitle>
          <CardDescription className="mt-2 text-sm leading-6">
            {invitation.email_required
              ? t("web.auth.invite.descriptionEmailRequired")
              : t("web.auth.invite.description")}
          </CardDescription>
          {invitation.note && (
            <blockquote className="border-border mt-4 border-s-2 py-1 ps-3 text-sm leading-relaxed">
              <span className="text-muted-foreground block text-xs">
                {invitation.inviter_name
                  ? t("web.auth.invite.noteFrom", { inviter: invitation.inviter_name })
                  : t("web.auth.invite.note")}
              </span>
              <span className="break-words whitespace-pre-line">{invitation.note}</span>
            </blockquote>
          )}
        </CardHeader>
        <CardContent>
          {appLink && (
            <div className="mb-6 space-y-3">
              <Button asChild size="lg" className="h-12 w-full text-base font-semibold">
                <a href={appLink}>
                  <Smartphone className="me-2 h-5 w-5" /> {t("web.auth.openInApp")}
                </a>
              </Button>
              <p className="text-muted-foreground text-center text-xs">
                {t("web.auth.invite.appMissing")}
              </p>
              <div className="flex items-center gap-3">
                <div className="border-border flex-1 border-t" />
                <span className="text-muted-foreground text-xs uppercase">
                  {t("web.auth.invite.orBrowser")}
                </span>
                <div className="border-border flex-1 border-t" />
              </div>
            </div>
          )}
          {createdUsername ? (
            <div role="status" className="space-y-4">
              <p>{t("web.auth.invite.created", { username: createdUsername })}</p>
              <Button asChild className="w-full">
                <Link to="/login">{t("web.auth.signIn")}</Link>
              </Button>
            </div>
          ) : (
            <>
              {!invitation.acceptance_available && (
                <p role="alert" className="mb-4">
                  {t("web.auth.invite.profileUnavailable")}
                </p>
              )}
              {localLoginDisabled && (
                <p role="alert" className="mb-4">
                  {invitation.inviter_name
                    ? t("web.auth.invite.localLoginDisabled", { inviter: invitation.inviter_name })
                    : t("web.auth.invite.localLoginDisabledNoInviter")}
                </p>
              )}
              {recovery && (
                <div role="alert" className="mb-4 space-y-3">
                  <p>{t("web.auth.invite.recovery")}</p>
                  <Button type="button" variant="outline" onClick={reloadLookup}>
                    {t("web.auth.invite.reload")}
                  </Button>
                </div>
              )}
              <form onSubmit={handleSubmit} className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="invite-email">{t("web.auth.email")}</Label>
                  {invitation.email_required ? (
                    <>
                      <Input
                        id="invite-email"
                        type="email"
                        value={email}
                        onChange={(e) => {
                          setEmail(e.target.value);
                          if (emailError) setEmailError("");
                        }}
                        disabled={submitting}
                        autoComplete="email"
                        placeholder={t("web.auth.invite.emailPlaceholder")}
                        aria-invalid={emailError ? true : undefined}
                        aria-describedby={emailError ? "invite-email-error" : undefined}
                        autoFocus={!appLink}
                        required
                      />
                      {emailError && (
                        <p id="invite-email-error" className="text-destructive text-xs">
                          {emailError}
                        </p>
                      )}
                    </>
                  ) : (
                    <Input id="invite-email" value={invitation.email} readOnly disabled />
                  )}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="invite-password">{t("web.auth.password")}</Label>
                  <p className="text-muted-foreground text-xs">
                    {t("web.auth.passwordRules.minLength")}
                  </p>
                  <PasswordInput
                    id="invite-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    disabled={submitting}
                    autoComplete="new-password"
                    // On mobile, focusing here pops the keyboard over the
                    // open-in-app button — the primary action when it's shown.
                    // A link invitation focuses the email field instead.
                    autoFocus={!appLink && !invitation.email_required}
                    required
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="invite-confirm-password">{t("web.auth.confirmPassword")}</Label>
                  <PasswordInput
                    id="invite-confirm-password"
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    disabled={submitting}
                    autoComplete="new-password"
                    required
                  />
                  {confirmPassword && password !== confirmPassword && (
                    <p className="text-destructive text-xs">
                      {t("web.auth.passwordRules.mismatch")}
                    </p>
                  )}
                </div>
                <Button
                  type="submit"
                  className="w-full"
                  disabled={
                    submitting || recovery || localLoginDisabled || !invitation.acceptance_available
                  }
                >
                  {submitting
                    ? t("web.auth.passwordRules.creatingAccount")
                    : t("web.auth.createAccount")}
                </Button>
              </form>
            </>
          )}
          <p className="text-muted-foreground mt-4 text-center text-sm">
            {t("web.auth.invite.alreadySetUp", {
              link: (chunks) => (
                <Link to="/login" className="text-foreground underline hover:no-underline">
                  {chunks}
                </Link>
              ),
            })}
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
