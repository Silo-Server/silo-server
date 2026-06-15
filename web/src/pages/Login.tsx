import { useCallback, useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import QRCode from "react-qr-code";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { api } from "@/api/client";
import type { DeviceLoginPollResponse, DeviceLoginStartResponse, Profile } from "@/api/types";
import { getBootstrapProfile, useAuth } from "@/hooks/useAuth";
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
import { toast } from "sonner";

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

export default function Login() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [provider, setProvider] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [startingDeviceLogin, setStartingDeviceLogin] = useState(false);
  const [deviceSession, setDeviceSession] = useState<DeviceLoginStartResponse | null>(null);
  const [deviceStatusMessage, setDeviceStatusMessage] = useState("");
  const [devicePolling, setDevicePolling] = useState(false);
  const [showDeviceFallback, setShowDeviceFallback] = useState(false);
  const {
    login,
    completeLogin,
    profile,
    selectProfile,
    user,
    loading,
    setupLoading,
    setupRequired,
    providers = [],
  } = useAuth();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { serverName, loginSubtitle } = useServerBranding();

  useDocumentTitle("Sign In");

  const redirectTarget = sanitizeAuthRedirect(searchParams.get("redirect"));

  const credentialProviders = useMemo(
    () => providers.filter((entry) => entry.mode === "credentials"),
    [providers],
  );
  const oauthProviders = useMemo(
    () => providers.filter((entry) => entry.mode === "oauth" && entry.installation_id),
    [providers],
  );

  const oauthError =
    searchParams.get("error") === "oauth_failed" ? searchParams.get("reason") : null;
  const nextParam = redirectTarget ? `?next=${encodeURIComponent(redirectTarget)}` : "";
  const selectedProvider =
    provider ||
    credentialProviders.find((entry) => entry.default)?.id ||
    credentialProviders[0]?.id ||
    "";

  const navigateAfterLogin = useCallback(async () => {
    if (redirectTarget) {
      navigate(redirectTarget, { replace: true });
      return;
    }

    try {
      const profileList = await api<{ profiles: Profile[] }>("/profiles");
      const soleProfile = getBootstrapProfile(profileList.profiles ?? []);
      if (soleProfile) {
        selectProfile(soleProfile);
        navigate("/");
        return;
      }
    } catch {
      navigate("/profiles");
      return;
    }
    navigate("/profiles");
  }, [navigate, redirectTarget, selectProfile]);

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
        const result = await api<DeviceLoginPollResponse>("/auth/device/poll", {
          method: "POST",
          body: JSON.stringify({ device_code: currentSession.device_code }),
        });
        if (cancelled) {
          return;
        }

        if (
          result.status === "approved" &&
          result.access_token &&
          result.refresh_token &&
          result.user
        ) {
          shouldPollAgain = false;
          completeLogin({
            access_token: result.access_token,
            refresh_token: result.refresh_token,
            expires_in: result.expires_in ?? 0,
            user: result.user,
          });
          setDeviceStatusMessage("Signed in. Loading profiles...");
          void navigateAfterLogin();
          return;
        }

        if (result.status === "denied") {
          shouldPollAgain = false;
          setDeviceStatusMessage("Approval was denied. Start a new code to try again.");
          setDeviceSession(null);
          return;
        }

        if (result.status === "expired" || result.status === "consumed") {
          shouldPollAgain = false;
          setDeviceStatusMessage("This code is no longer valid. Start a new one.");
          setDeviceSession(null);
          return;
        }

        setDeviceStatusMessage("Waiting for approval on your phone...");
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

  if (loading || setupLoading) {
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

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    try {
      await login(username, password, selectedProvider || undefined);
      await navigateAfterLogin();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Login failed");
    } finally {
      setSubmitting(false);
    }
  }

  async function handleStartDeviceLogin() {
    setStartingDeviceLogin(true);
    try {
      const data = await api<DeviceLoginStartResponse>("/auth/device/start", {
        method: "POST",
        body: JSON.stringify(buildDevicePayload()),
      });
      setDeviceSession(data);
      setDeviceStatusMessage("Waiting for approval on your phone...");
      setShowDeviceFallback(false);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to start device login");
    } finally {
      setStartingDeviceLogin(false);
    }
  }

  const signupHref = redirectTarget
    ? `/signup?redirect=${encodeURIComponent(redirectTarget)}`
    : "/signup";

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
          {oauthError && (
            <div className="border-destructive/30 bg-destructive/10 text-destructive rounded-md border p-3 text-sm">
              Sign-in failed: {decodeURIComponent(oauthError)}
            </div>
          )}
          {oauthProviders.length > 0 && (
            <div className="space-y-2">
              {oauthProviders.map((entry) => (
                <form
                  key={entry.id}
                  method="post"
                  action={`/api/v1/auth/oauth/${entry.installation_id}/init${nextParam}`}
                >
                  <Button type="submit" variant="outline" className="w-full justify-start gap-3">
                    {entry.icon_url && <img src={entry.icon_url} alt="" className="h-5 w-5" />}
                    <span>{entry.display_name}</span>
                  </Button>
                </form>
              ))}
              <div className="text-muted-foreground flex items-center gap-2 pt-2 text-xs">
                <div className="bg-border h-px flex-1" />
                <span>or</span>
                <div className="bg-border h-px flex-1" />
              </div>
            </div>
          )}
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
              <Label htmlFor="password">Password</Label>
              <PasswordInput
                id="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                required
              />
            </div>
            {credentialProviders.length > 1 && (
              <div className="space-y-2">
                <Label>Sign in with</Label>
                <Select value={selectedProvider} onValueChange={setProvider}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {credentialProviders.map((entry) => (
                      <SelectItem key={entry.id} value={entry.id}>
                        {entry.display_name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting ? "Signing in..." : "Sign in"}
            </Button>
          </form>

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
                  <div className="space-y-2 text-center">
                    <div>
                      <div className="text-muted-foreground text-xs tracking-[0.12em] uppercase">
                        Match code
                      </div>
                      <div className="text-lg font-semibold">{deviceSession.match_code}</div>
                    </div>
                    {showDeviceFallback ? (
                      <div className="space-y-2">
                        <div>
                          <div className="text-muted-foreground text-xs tracking-[0.12em] uppercase">
                            Enter this code if needed
                          </div>
                          <div className="font-mono text-lg font-semibold">
                            {deviceSession.user_code}
                          </div>
                        </div>
                        <p className="text-muted-foreground text-xs break-all">
                          {deviceSession.verification_uri}
                        </p>
                      </div>
                    ) : (
                      <Button
                        type="button"
                        variant="ghost"
                        className="text-muted-foreground hover:text-foreground mx-auto h-auto px-0 py-1 text-xs"
                        onClick={() => setShowDeviceFallback(true)}
                      >
                        Can&apos;t scan the QR code?
                      </Button>
                    )}
                  </div>
                  <div className="space-y-2">
                    <Button
                      type="button"
                      variant="outline"
                      className="w-full"
                      onClick={() => {
                        setDeviceSession(null);
                        setDeviceStatusMessage("");
                        setShowDeviceFallback(false);
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

          <p className="text-muted-foreground text-center text-sm">
            Don&apos;t have an account?{" "}
            <Link to={signupHref} className="text-foreground underline hover:no-underline">
              Sign up
            </Link>
          </p>
        </CardContent>
      </Card>
    </main>
  );
}
