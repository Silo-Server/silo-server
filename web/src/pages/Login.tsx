import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import QRCode from "react-qr-code";
import { Link, Navigate, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { sessionFromTokenPair } from "@/api/v2/account";
import { v2, V2ProblemError, type V2Result } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";
import { usePasswordResetAvailable } from "@/hooks/queries/passwordReset";
import { CHANGE_PASSWORD_PATH, usePostSignInNavigation } from "@/hooks/usePostSignInNavigation";
import { Button } from "@/components/ui/button";
import { PasswordInput } from "@/components/PasswordInput";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useServerBranding } from "@/hooks/useServerBranding";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { sanitizeAuthRedirect } from "@/lib/authRedirect";
import { formatDeviceCode, spokenDeviceCode } from "@/lib/deviceCode";
import {
  clearSignedOut,
  leaveForProvider,
  networkIdentityName,
  networkSignInRefusalText,
  oauthFailureText,
  oauthStartHref,
  wasSignedOut,
} from "@/lib/externalSignIn";
import { toast } from "sonner";

type DeviceLoginSession = V2Result<"POST /api/v2/auth/device/start">;

function detectPlatform() {
  const ua = navigator.userAgent;
  if (/AppleTV|tvOS/i.test(ua)) return "tvOS";
  if (/Android TV|GoogleTV/i.test(ua)) return "Android TV";
  if (/Tizen/i.test(ua)) return "Tizen";
  if (/Web0S|webOS|WebOS/i.test(ua)) return "webOS";
  if (/Roku/i.test(ua)) return "Roku";
  if (/Xbox/i.test(ua)) return "Xbox";
  if (/PlayStation/i.test(ua)) return "PlayStation";
  if (/iPad/i.test(ua)) return "iPadOS";
  if (/iPhone/i.test(ua)) return "iOS";
  if (/Android/i.test(ua)) return "Android";
  if (/Macintosh|Mac OS X/i.test(ua)) return "macOS";
  if (/Windows/i.test(ua)) return "Windows";
  if (/Linux/i.test(ua)) return "Linux";
  return "Browser";
}

function detectBrowser() {
  const ua = navigator.userAgent;
  if (/Edg\//i.test(ua)) return "Edge";
  if (/Chrome\//i.test(ua) && !/Edg\//i.test(ua)) return "Chrome";
  if (/Firefox\//i.test(ua)) return "Firefox";
  if (/Safari\//i.test(ua) && !/Chrome\//i.test(ua)) return "Safari";
  return "Browser";
}

function buildDevicePayload() {
  const platform = detectPlatform();
  const browser = detectBrowser();
  const isBigScreen =
    /tv|roku|playstation|xbox|tizen|webos/i.test(platform) ||
    Math.max(window.innerWidth, window.innerHeight) >= 1600;

  return {
    device_name: isBigScreen ? `${platform} TV` : `${browser} on ${platform}`,
    device_platform: platform,
  };
}

// Picker value for "let the server route by account" (no provider sent).
const AUTO_PROVIDER = "auto";

/**
 * Withdraws a pairing request nobody will finish (D4), so the approver's
 * phone shows it as canceled instead of a live code. Best effort: the
 * request expires on its own anyway.
 */
function cancelDeviceLogin(deviceCode: string) {
  void v2("POST /api/v2/auth/device/cancel", { body: { device_code: deviceCode } }).catch(
    () => undefined,
  );
}

export default function Login() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [provider, setProvider] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [startingDeviceLogin, setStartingDeviceLogin] = useState(false);
  const [deviceSession, setDeviceSession] = useState<DeviceLoginSession | null>(null);
  const [deviceStatusMessage, setDeviceStatusMessage] = useState("");
  const [devicePolling, setDevicePolling] = useState(false);
  // The device code of a request still waiting for approval, which leaving
  // the page or starting over withdraws.
  const pendingDeviceCode = useRef<string | null>(null);
  const providerPickerId = useId();
  const providerPickerHintId = useId();
  const [loginRefusal, setLoginRefusal] = useState<string | null>(null);
  // Read once: a sign-out in this tab keeps the page from sending the person
  // straight back to the provider (see markSignedOut).
  const [signedOutHere] = useState(wasSignedOut);
  const autoRedirected = useRef(false);
  const [providersReady, setProvidersReady] = useState(false);
  const {
    login,
    completeLogin,
    profile,
    user,
    pendingPasswordChange,
    loading,
    setupLoading,
    setupRequired,
    providers = [],
    refreshSignInProviders,
    sessionRestoreUnavailable = false,
    sessionRestoreProviderUnavailable = false,
    retrySessionRestore,
  } = useAuth();
  const [searchParams] = useSearchParams();
  const { serverName, loginSubtitle } = useServerBranding();
  // Always refetch on mount so a cached answer from before an admin closed
  // signups can't show the link, and show it only from that fresh result.
  const signupStatusQuery = useQuery({
    queryKey: ["auth", "signup-status"],
    queryFn: () => v2("GET /api/v2/auth/signup"),
    refetchOnMount: "always",
  });
  const signupOpen =
    signupStatusQuery.isSuccess &&
    signupStatusQuery.isFetchedAfterMount &&
    signupStatusQuery.data.enabled;
  const { available: passwordResetAvailable } = usePasswordResetAvailable();

  useDocumentTitle("Sign In");

  const redirectTarget = sanitizeAuthRedirect(searchParams.get("redirect"));

  // Another browser may have changed the server's provider since this tab
  // started. Read it again before choosing a form or redirecting automatically.
  useEffect(() => {
    let canceled = false;
    if (!refreshSignInProviders) {
      setProvidersReady(true);
      return;
    }
    void refreshSignInProviders().finally(() => {
      if (!canceled) setProvidersReady(true);
    });
    return () => {
      canceled = true;
    };
  }, [refreshSignInProviders]);

  const credentialProviders = useMemo(
    () => providers.filter((entry) => entry.mode === "credentials"),
    [providers],
  );
  const oauthProviders = useMemo(
    () => providers.filter((entry) => entry.mode === "oauth" && entry.installation_id),
    [providers],
  );
  // A network provider (such as Tailscale) is listed only when this browser
  // reached the server through it, with the device owner's name.
  const networkProviders = useMemo(
    () => providers.filter((entry) => entry.mode === "network" && entry.installation_id),
    [providers],
  );
  const [networkSigningIn, setNetworkSigningIn] = useState<string | null>(null);

  const oauthError =
    searchParams.get("error") === "oauth_failed"
      ? searchParams.get("reason") || "login_failed"
      : null;
  // /login?local=1 shows the password form even while local password sign-in
  // is off, for break-glass admins.
  const localBypass = searchParams.get("local") === "1";
  // Set by "Not you? Switch account": the provider is asked to let the
  // person pick another provider account instead of reusing its session.
  const switchingAccount = searchParams.get("switch_account") === "1";
  // With a directory (LDAP) provider the server routes a password sign-in
  // that names no provider by account: accounts with a local password sign in
  // locally (their password never reaches the directory), everyone else goes
  // to the directory. Default to that instead of preselecting one provider,
  // which would fail directory users on Local, or send a break-glass admin's
  // password to the directory while local sign-in is off.
  const hasDirectoryProvider = credentialProviders.some((entry) => entry.id !== "local");
  const selectedProvider =
    provider ||
    (hasDirectoryProvider ? AUTO_PROVIDER : undefined) ||
    credentialProviders.find((entry) => entry.default)?.id ||
    credentialProviders[0]?.id ||
    "";

  // With local password sign-in off and no directory, only the OAuth or
  // network provider can sign anyone in, so the password form is hidden. It
  // stays when the provider list could not be read (no provider at all).
  const passwordFormShown =
    localBypass ||
    credentialProviders.length > 0 ||
    (oauthProviders.length === 0 && networkProviders.length === 0);
  const startHref = (installationId: string) =>
    oauthStartHref(installationId, { next: redirectTarget, selectAccount: switchingAccount });
  // The only way in is one OAuth provider: go there directly, unless the
  // person asked for the password form, just came back from a failure, is
  // switching account or signed out in this tab (the provider may still
  // have its own session and would sign them straight back in).
  const autoRedirectProvider =
    !passwordFormShown &&
    oauthProviders.length === 1 &&
    networkProviders.length === 0 &&
    !searchParams.has("error") &&
    !switchingAccount &&
    !signedOutHere &&
    !sessionRestoreUnavailable
      ? oauthProviders[0]
      : null;
  const autoRedirectHref = autoRedirectProvider?.installation_id
    ? startHref(autoRedirectProvider.installation_id)
    : null;
  const readyToRedirect =
    providersReady &&
    !loading &&
    !setupLoading &&
    !setupRequired &&
    !user &&
    !pendingPasswordChange;

  useEffect(() => {
    if (!autoRedirectHref || !readyToRedirect || autoRedirected.current) {
      return;
    }
    autoRedirected.current = true;
    leaveForProvider(autoRedirectHref);
  }, [autoRedirectHref, readyToRedirect]);

  const navigateAfterLogin = usePostSignInNavigation(redirectTarget);

  useEffect(() => {
    if (!deviceSession) {
      return;
    }

    const currentSession = deviceSession;
    let cancelled = false;
    const intervalMs = Math.max(1, currentSession.interval || 3) * 1000;
    let timerId: number | null = null;

    async function poll() {
      let shouldPollAgain = true;
      try {
        setDevicePolling(true);
        const result = await v2("POST /api/v2/auth/device/poll", {
          body: { device_code: currentSession.device_code },
        });
        if (cancelled) {
          return;
        }

        if (result.status === "approved" && result.tokens) {
          shouldPollAgain = false;
          pendingDeviceCode.current = null;
          const session = sessionFromTokenPair(result.tokens);
          completeLogin(session);
          setDeviceStatusMessage("Signed in. Loading profiles...");
          void navigateAfterLogin(session.user);
          return;
        }

        if (result.status === "denied") {
          shouldPollAgain = false;
          pendingDeviceCode.current = null;
          setDeviceStatusMessage("Approval was denied. Start a new code to try again.");
          setDeviceSession(null);
          return;
        }

        if (
          result.status === "expired" ||
          result.status === "consumed" ||
          result.status === "canceled"
        ) {
          shouldPollAgain = false;
          pendingDeviceCode.current = null;
          setDeviceStatusMessage("This code is no longer valid. Start a new one.");
          setDeviceSession(null);
          return;
        }

        // Someone opened the link on a phone: the person carries on there.
        setDeviceStatusMessage(
          result.opened ? "Continue on your phone." : "Waiting for approval on your phone...",
        );
      } catch (error) {
        if (!cancelled) {
          setDeviceStatusMessage(error instanceof Error ? error.message : "Device sign-in failed");
        }
      } finally {
        if (!cancelled) {
          setDevicePolling(false);
          if (shouldPollAgain) {
            timerId = window.setTimeout(poll, intervalMs);
          }
        }
      }
    }

    void poll();
    return () => {
      cancelled = true;
      if (timerId !== null) {
        window.clearTimeout(timerId);
      }
    };
  }, [completeLogin, deviceSession, navigateAfterLogin]);

  // Leaving the page withdraws a request still waiting for approval.
  useEffect(
    () => () => {
      if (pendingDeviceCode.current) cancelDeviceLogin(pendingDeviceCode.current);
    },
    [],
  );

  if (loading || setupLoading || !providersReady) {
    return (
      <main className="auth-shell">
        <AuthBackground />
        <div
          className="border-primary h-8 w-8 animate-spin rounded-full border-b-2"
          role="status"
          aria-label="Loading"
        />
      </main>
    );
  }

  if (setupRequired && !user) {
    return <Navigate to="/setup" replace />;
  }

  if (user) {
    return <Navigate to={redirectTarget || (profile ? "/" : "/profiles")} replace />;
  }
  if (pendingPasswordChange) {
    const redirect = redirectTarget ? `?redirect=${encodeURIComponent(redirectTarget)}` : "";
    return <Navigate to={`${CHANGE_PASSWORD_PATH}${redirect}`} replace />;
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setLoginRefusal(null);
    try {
      const signedIn = await login(
        username,
        password,
        selectedProvider && selectedProvider !== AUTO_PROVIDER ? selectedProvider : undefined,
      );
      await navigateAfterLogin(signedIn);
    } catch (err) {
      const refusal = err instanceof V2ProblemError ? passwordLoginRefusal(err.problemType) : null;
      if (refusal) {
        setLoginRefusal(refusal);
      } else {
        toast.error(err instanceof Error ? err.message : "Login failed");
      }
    } finally {
      setSubmitting(false);
    }
  }

  async function handleNetworkSignIn(entry: (typeof networkProviders)[number]) {
    setNetworkSigningIn(entry.id);
    setLoginRefusal(null);
    try {
      const pair = await v2("POST /api/v2/auth/network/{id}/sign-in", {
        path: { id: entry.installation_id ?? "" },
        body: {},
        retryAuthentication: false,
      });
      clearSignedOut();
      const session = sessionFromTokenPair(pair);
      completeLogin(session);
      await navigateAfterLogin(session.user);
    } catch (err) {
      const refusal =
        err instanceof V2ProblemError
          ? networkSignInRefusalText(err.problemType, entry.display_name)
          : null;
      if (refusal) {
        setLoginRefusal(refusal);
      } else {
        toast.error(err instanceof Error ? err.message : "Sign-in failed");
      }
    } finally {
      setNetworkSigningIn(null);
    }
  }

  async function handleStartDeviceLogin() {
    setStartingDeviceLogin(true);
    try {
      const data = await v2("POST /api/v2/auth/device/start", { body: buildDevicePayload() });
      pendingDeviceCode.current = data.device_code;
      setDeviceSession(data);
      setDeviceStatusMessage("Waiting for approval on your phone...");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to start device login");
    } finally {
      setStartingDeviceLogin(false);
    }
  }

  const signupHref = redirectTarget
    ? `/signup?redirect=${encodeURIComponent(redirectTarget)}`
    : "/signup";
  // Only a local password can be reset here; an external provider owns its
  // own. So the link shows only while the form is explicitly the local one:
  // the local provider picked (or the only one), or /login?local=1 with no
  // password provider listed. Automatic routing may send the name to a
  // directory, and there is no form when the provider is the only way in.
  const forgotPasswordShown =
    passwordResetAvailable &&
    passwordFormShown &&
    (!selectedProvider || selectedProvider === "local");
  const oauthProviderNames = oauthProviders.map((entry) => entry.display_name).join(" or ");
  function passwordLoginRefusal(problemType: string): string | null {
    switch (problemType) {
      case "local_login_disabled":
        return oauthProviderNames
          ? `Password sign-in is turned off on this server. Sign in with ${oauthProviderNames} instead.`
          : "Password sign-in is turned off on this server.";
      case "password_expired":
        return "Your directory password has expired. Change it with your organization, then sign in again.";
      case "permission_denied":
        // Sign-in has no caller yet, so the only denial is a disabled account.
        return oauthFailureText("account_disabled");
      case "not_permitted":
      case "account_required":
      case "email_in_use":
      case "identity_linked_elsewhere":
      case "provider_unavailable":
        return oauthFailureText(problemType);
      default:
        return null;
    }
  }
  const forgotPasswordHref = username.trim()
    ? `/forgot-password?login=${encodeURIComponent(username.trim())}`
    : "/forgot-password";

  return (
    <main className="auth-shell">
      <AuthBackground />
      <h1 className="sr-only">Sign in to {serverName}</h1>
      <Card className="auth-card glass panel-border w-full max-w-md border-0">
        <CardHeader>
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">{serverName}</CardTitle>
          <CardDescription className="mt-2 text-sm leading-6">{loginSubtitle}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          {sessionRestoreUnavailable && (
            <div
              role="alert"
              className="border-border bg-muted/40 space-y-2 rounded-md border p-3 text-sm"
            >
              <p>
                {/* Only a 503 provider_unavailable is the provider's; any
                    other outage is the server's or the network's. */}
                {sessionRestoreProviderUnavailable
                  ? "Can't reach the sign-in provider right now."
                  : "Can't restore your session right now."}{" "}
                You&apos;re still signed in; try again in a moment.
              </p>
              <Button type="button" variant="outline" size="sm" onClick={retrySessionRestore}>
                Try again
              </Button>
            </div>
          )}
          {oauthError && (
            <div
              role="alert"
              className="border-destructive/30 bg-destructive/10 text-destructive rounded-md border p-3 text-sm"
            >
              {oauthFailureText(oauthError)}
            </div>
          )}
          {loginRefusal && (
            <div
              role="alert"
              className="border-destructive/30 bg-destructive/10 text-destructive rounded-md border p-3 text-sm"
            >
              {loginRefusal}
            </div>
          )}
          {autoRedirectProvider && readyToRedirect && (
            <p className="text-muted-foreground text-sm" role="status">
              Taking you to {autoRedirectProvider.display_name}…
            </p>
          )}
          {switchingAccount && oauthProviders.length > 0 && (
            <p className="text-muted-foreground text-sm">
              Choose the account to sign in with. You may be asked which account to use at the
              sign-in provider.
            </p>
          )}
          {networkProviders.length > 0 && (
            <div className="space-y-2">
              {networkProviders.map((entry) => {
                const owner = networkIdentityName(entry);
                let label = `Continue with ${entry.display_name}`;
                if (networkSigningIn === entry.id) label = "Signing in…";
                else if (owner) label = `Continue as ${owner}`;
                return (
                  <Button
                    key={entry.id}
                    type="button"
                    className="h-auto w-full justify-start gap-3 py-2"
                    disabled={networkSigningIn !== null}
                    onClick={() => void handleNetworkSignIn(entry)}
                  >
                    {entry.icon_url && <img src={entry.icon_url} alt="" className="h-5 w-5" />}
                    <span className="flex flex-col items-start text-left">
                      <span>{label}</span>
                      {owner && (
                        <span className="text-xs font-normal opacity-80">
                          via {entry.display_name}
                        </span>
                      )}
                    </span>
                  </Button>
                );
              })}
              {(passwordFormShown || oauthProviders.length > 0) && (
                <div className="text-muted-foreground flex items-center gap-2 pt-2 text-xs">
                  <div className="bg-border h-px flex-1" />
                  <span>or</span>
                  <div className="bg-border h-px flex-1" />
                </div>
              )}
            </div>
          )}
          {oauthProviders.length > 0 && (
            <div className="space-y-2">
              {oauthProviders.map((entry) => (
                <Button
                  key={entry.id}
                  asChild
                  variant="outline"
                  className="w-full justify-start gap-3"
                >
                  <a href={startHref(entry.installation_id ?? "")} onClick={() => clearSignedOut()}>
                    {entry.icon_url && <img src={entry.icon_url} alt="" className="h-5 w-5" />}
                    <span>{entry.display_name}</span>
                  </a>
                </Button>
              ))}
              {passwordFormShown && (
                <div className="text-muted-foreground flex items-center gap-2 pt-2 text-xs">
                  <div className="bg-border h-px flex-1" />
                  <span>or</span>
                  <div className="bg-border h-px flex-1" />
                </div>
              )}
            </div>
          )}
          {passwordFormShown && (
            <form onSubmit={handleSubmit} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="username">Username</Label>
                <Input
                  id="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoComplete="username"
                  autoFocus
                  required
                />
              </div>
              <div className="space-y-2">
                <div className="flex items-baseline justify-between gap-2">
                  <Label htmlFor="password">Password</Label>
                  {forgotPasswordShown && (
                    <Link
                      to={forgotPasswordHref}
                      className="text-muted-foreground hover:text-foreground text-xs underline-offset-4 hover:underline"
                    >
                      Forgot password?
                    </Link>
                  )}
                </div>
                <PasswordInput
                  id="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="current-password"
                  required
                />
              </div>
              {(credentialProviders.length > 1 || hasDirectoryProvider) && (
                <div className="space-y-2">
                  <Label htmlFor={providerPickerId}>Sign in with</Label>
                  <Select value={selectedProvider} onValueChange={setProvider}>
                    <SelectTrigger
                      id={providerPickerId}
                      className="w-full"
                      aria-describedby={hasDirectoryProvider ? providerPickerHintId : undefined}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {hasDirectoryProvider && (
                        <SelectItem value={AUTO_PROVIDER}>Automatic</SelectItem>
                      )}
                      {credentialProviders.map((entry) => (
                        <SelectItem key={entry.id} value={entry.id}>
                          {entry.display_name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {hasDirectoryProvider ? (
                    <p id={providerPickerHintId} className="text-muted-foreground text-xs">
                      Automatic: accounts with a Silo password sign in with it, everyone else with
                      the directory.
                    </p>
                  ) : null}
                </div>
              )}
              <Button type="submit" className="w-full" disabled={submitting}>
                {submitting ? "Signing in..." : "Sign in"}
              </Button>
            </form>
          )}

          <div className="space-y-4">
            <Separator />
            <div className="space-y-3">
              <div>
                <h2 className="text-sm font-semibold">Use your phone instead</h2>
                <p className="text-muted-foreground mt-1 text-sm">
                  Scan a code, sign in there, and approve this device.
                </p>
              </div>
              {!deviceSession ? (
                <Button
                  type="button"
                  variant="outline"
                  className="w-full"
                  disabled={startingDeviceLogin}
                  onClick={() => void handleStartDeviceLogin()}
                >
                  {startingDeviceLogin ? "Generating code..." : "Show QR code"}
                </Button>
              ) : (
                <div className="border-border/60 bg-background/50 space-y-4 rounded-md border p-4">
                  <div className="flex justify-center rounded-md bg-white p-3">
                    <QRCode value={deviceSession.verification_uri_complete} size={176} />
                  </div>
                  <div className="space-y-1 text-center">
                    <p className="text-muted-foreground text-sm">
                      Or go to{" "}
                      <span className="text-foreground break-all">
                        {deviceSession.verification_uri}
                      </span>{" "}
                      and enter
                    </p>
                    <div
                      className="font-mono text-2xl font-semibold tracking-[0.12em]"
                      aria-hidden="true"
                    >
                      {formatDeviceCode(deviceSession.user_code)}
                    </div>
                    <span className="sr-only">{spokenDeviceCode(deviceSession.user_code)}</span>
                  </div>
                  <div className="space-y-2">
                    <Button
                      type="button"
                      variant="outline"
                      className="w-full"
                      onClick={() => {
                        if (pendingDeviceCode.current) {
                          cancelDeviceLogin(pendingDeviceCode.current);
                          pendingDeviceCode.current = null;
                        }
                        setDeviceSession(null);
                        setDeviceStatusMessage("");
                      }}
                    >
                      Start over
                    </Button>
                    <p className="text-muted-foreground text-center text-sm">
                      {devicePolling ? "Checking for approval..." : deviceStatusMessage}
                    </p>
                  </div>
                </div>
              )}
            </div>
          </div>

          {signupOpen && (
            <p className="text-muted-foreground text-center text-sm">
              Don&apos;t have an account?{" "}
              <Link to={signupHref} className="text-foreground underline hover:no-underline">
                Sign up
              </Link>
            </p>
          )}
        </CardContent>
      </Card>
    </main>
  );
}
