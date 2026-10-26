import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { v2, V2ProblemError, type V2Result } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useBranding } from "@/hooks/useBranding";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { formatRelativeTime } from "@/lib/date";
import { buildDeviceDeepLink, detectMobilePlatform } from "@/lib/appDeepLink";
import {
  QUICK_CONNECT_CODE_LENGTH,
  compactDeviceCode,
  displayDeviceCode,
  formatDeviceCode,
  isCompleteDeviceCode,
  spokenDeviceCode,
  tooManyCodesMessage,
} from "@/lib/deviceCode";
import { QuickConnectApproval } from "@/components/auth/QuickConnectApproval";
import { Smartphone } from "lucide-react";

type DeviceLoginDetails = V2Result<"GET /api/v2/auth/device">;

type LoadError = "not_found" | "failed" | "rate_limited" | "impersonating";

// After approving, the page watches the request until it settles: the TV
// collects its session, or the request is denied, canceled or expires. The
// TV polls every few seconds, so this usually settles quickly; after the
// first minutes the page checks less often.
const WATCH_INTERVAL_MS = 3000;
const WATCH_SLOW_INTERVAL_MS = 15000;
const WATCH_FAST_MS = 2 * 60 * 1000;

function platformLabel(platform: string) {
  switch (platform.toLowerCase()) {
    case "tvos":
      return "Apple TV";
    case "android-tv":
    case "androidtv":
      return "Android TV";
    default:
      return platform;
  }
}

export default function ActivateDevice() {
  const [searchParams] = useSearchParams();
  const token = searchParams.get("token") ?? "";
  const code = compactDeviceCode(searchParams.get("code") ?? "");
  // Each request owns its lookups, decisions and watch state. A response
  // for a previous code cannot update the next request's card.
  return <ActivateDeviceRequest key={token ? `token:${token}` : `code:${code}`} />;
}

function ActivateDeviceRequest() {
  const { user, loading, setupLoading, logoutOfSiloOnly, isImpersonating, profile } = useAuth();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [codeInput, setCodeInput] = useState(formatDeviceCode(searchParams.get("code") ?? ""));
  const [details, setDetails] = useState<DeviceLoginDetails | null>(null);
  const [loadError, setLoadError] = useState<LoadError | null>(null);
  const [retryAfter, setRetryAfter] = useState<number | null>(null);
  const [loadingDetails, setLoadingDetails] = useState(false);
  const [acting, setActing] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [approvedHere, setApprovedHere] = useState(false);
  const [approvedProfile, setApprovedProfile] = useState<string | null>(null);
  // Counts watch lookups, so a failed one (details unchanged) still
  // schedules the next.
  const [watchTick, setWatchTick] = useState(0);
  const [focusResult, setFocusResult] = useState(false);
  const watchStarted = useRef<number | null>(null);
  const resultRef = useRef<HTMLParagraphElement>(null);
  const { serverName: brandedName } = useBranding();

  useDocumentTitle("Sign in a TV");

  const token = searchParams.get("token") ?? "";
  const code = compactDeviceCode(searchParams.get("code") ?? "");
  // A six-digit Jellyfin Quick Connect code is found only for a signed-in
  // account, so it is looked up once someone is signed in.
  const needsAccount = !token && code.length === QUICK_CONNECT_CODE_LENGTH;
  const lookupAccount = needsAccount ? (user?.username ?? null) : null;

  const redirectTarget = useMemo(() => {
    const query = searchParams.toString();
    return query ? `/activate?${query}` : "/activate";
  }, [searchParams]);
  const loginHref = `/login?redirect=${encodeURIComponent(redirectTarget)}`;

  // Numbers the lookups, so only the newest answer updates the card: a watch
  // lookup still in flight when a decision lands must not replace its result.
  const lookupSeq = useRef(0);
  // keepOnError leaves the last known details on screen when a refresh
  // fails, for the reloads after a decision and while watching. Returns the
  // details it loaded, or null.
  const loadDetails = useCallback(
    async (keepOnError = false): Promise<DeviceLoginDetails | null> => {
      const seq = ++lookupSeq.current;
      if ((!token && !code) || (needsAccount && lookupAccount === null)) {
        setDetails(null);
        setLoadError(null);
        return null;
      }
      try {
        const result = await v2("GET /api/v2/auth/device", {
          query: token ? { token } : { code },
        });
        if (seq === lookupSeq.current) {
          setDetails(result);
          setLoadError(null);
        }
        return result;
      } catch (error) {
        if (keepOnError || seq !== lookupSeq.current) {
          return null;
        }
        setDetails(null);
        const status = error instanceof V2ProblemError ? error.status : 0;
        // A Quick Connect code spends the account's code budget.
        setRetryAfter(error instanceof V2ProblemError ? error.retryAfterSeconds : null);
        setLoadError(
          status === 404
            ? "not_found"
            : status === 429
              ? "rate_limited"
              : status === 403
                ? "impersonating"
                : "failed",
        );
        return null;
      }
    },
    [code, token, needsAccount, lookupAccount],
  );

  useEffect(() => {
    setLoadingDetails(true);
    void loadDetails().finally(() => setLoadingDetails(false));
  }, [loadDetails]);

  // Follow a request approved here, or a pending remote-playback request,
  // until it settles. The server answers expired once expires_at passes, so
  // this always ends.
  const status = details?.status;
  // Every Quick Connect lookup spends the account's code budget, so a Quick
  // Connect approval is not watched.
  const isQuickConnect = details?.client_purpose === "jellyfin_quick_connect";
  // This page approves a TV sign-in or a Quick Connect code. A remote-playback
  // request comes from a Silo app sending playback to the TV, and that app
  // approves it for its own profile, so a pending one is followed too.
  const remotePlayback =
    details !== null &&
    !isQuickConnect &&
    (details.client_purpose !== "device_login" || details.temporary);
  const watching =
    !isQuickConnect &&
    ((approvedHere && status === "approved") || (remotePlayback && status === "pending"));
  useEffect(() => {
    if (!watching) {
      return;
    }
    watchStarted.current ??= Date.now();
    const delay =
      Date.now() - watchStarted.current < WATCH_FAST_MS
        ? WATCH_INTERVAL_MS
        : WATCH_SLOW_INTERVAL_MS;
    const timer = window.setTimeout(() => {
      void loadDetails(true).finally(() => setWatchTick((tick) => tick + 1));
    }, delay);
    return () => window.clearTimeout(timer);
  }, [watching, details, watchTick, loadDetails]);

  // After a decision the focused button is gone; move focus to the result.
  useEffect(() => {
    if (focusResult && status && status !== "pending") {
      resultRef.current?.focus();
      setFocusResult(false);
    }
  }, [focusResult, status]);

  async function handleDecision(action: "approve" | "deny") {
    setActing(true);
    setActionError(null);
    const failure =
      action === "approve" ? "Couldn't sign in the TV. Try again." : "Couldn't decline. Try again.";
    // Every Quick Connect lookup spends the account's code budget, so a
    // decision on one is followed by a reload only when the request moved on.
    let reload = !isQuickConnect;
    let refused = false;
    try {
      const body = token ? { token } : { code };
      if (action === "approve") {
        await v2("POST /api/v2/auth/device/approve", { body });
        // The reload below may fail; the watch still has to start.
        setDetails((current) => (current ? { ...current, status: "approved" } : current));
        setApprovedHere(true);
      } else {
        await v2("POST /api/v2/auth/device/deny", { body });
        if (isQuickConnect) {
          setDetails((current) => (current ? { ...current, status: "denied" } : current));
        }
      }
    } catch (error) {
      // A conflict or gone usually means the request moved on; the reload
      // shows why.
      const moved = error instanceof V2ProblemError && [404, 409, 410].includes(error.status);
      reload ||= moved;
      if (error instanceof V2ProblemError && error.status === 429) {
        setActionError(tooManyCodesMessage(error.retryAfterSeconds));
      } else if (
        error instanceof V2ProblemError &&
        error.status === 403 &&
        error.problemType === "permission_denied"
      ) {
        // Only the account's own login session decides for a TV, never an
        // admin viewing as someone; trying again cannot help.
        setActionError(impersonationText);
      } else if (moved) {
        // If the request is still pending after the reload, the server
        // refused the decision itself.
        refused = true;
      } else {
        setActionError(failure);
      }
    } finally {
      if (reload) {
        const current = await loadDetails(true);
        if (refused && (current === null || current.status === "pending")) {
          setActionError(failure);
        }
      }
      setActing(false);
      setFocusResult(true);
    }
  }

  function handleCodeSubmit(e: FormEvent) {
    e.preventDefault();
    const compact = compactDeviceCode(codeInput);
    if (!isCompleteDeviceCode(compact)) {
      return;
    }
    setSearchParams({ code: compact });
  }

  function quickConnectApproved(profileName: string) {
    setDetails((current) => (current ? { ...current, status: "approved" } : current));
    setApprovedHere(true);
    setApprovedProfile(profileName);
    setFocusResult(true);
  }

  function quickConnectMoved() {
    void loadDetails(true).finally(() => setFocusResult(true));
  }

  function enterAnotherCode() {
    setApprovedHere(false);
    setApprovedProfile(null);
    watchStarted.current = null;
    setFocusResult(false);
    setActionError(null);
    setCodeInput("");
    setSearchParams({});
  }

  // Signs out of Silo only: ending the provider's session too would sign
  // the person out of everything else that uses it. The login page then asks
  // the provider to let them pick another account (prompt=select_account)
  // and comes back here with the code.
  function switchAccount() {
    logoutOfSiloOnly();
    navigate(`${loginHref}&switch_account=1`);
  }

  const serverName = details?.server_name || brandedName;
  const impersonationText = `Stop viewing as ${user?.username ?? "this user"} to approve a TV. Only the account itself can sign in a TV.`;
  // A tapped link into the phone app, which approves with its own session.
  // Offered only where a Silo app exists, and never opened automatically.
  const appLink =
    details && detectMobilePlatform(navigator.userAgent)
      ? buildDeviceDeepLink(details.server_id, window.location.origin, details.user_code || code)
      : null;
  const serverHost = window.location.host;
  const deviceName = details?.device_name || "this device";
  const shownCode = displayDeviceCode(details?.user_code || code);
  const screen = isQuickConnect ? "app" : "TV";
  const requestedAgo = formatRelativeTime(details?.requested_at);
  const deviceLine = [
    details?.device_platform ? platformLabel(details.device_platform) : "",
    requestedAgo ? `requested ${requestedAgo}` : "",
  ]
    .filter(Boolean)
    .join(" · ");

  let resultMessage = "";
  switch (details?.status) {
    case "approved":
      if (remotePlayback) {
        resultMessage = "This request was approved in the Silo app.";
      } else if (isQuickConnect) {
        resultMessage = approvedProfile
          ? `Done. ${deviceName} is signing in as ${approvedProfile}.`
          : "This device was approved and is finishing sign-in.";
      } else {
        resultMessage = approvedHere
          ? "Done. Your TV is signing in."
          : "This TV was approved and is finishing sign-in.";
      }
      break;
    case "consumed":
      if (remotePlayback) {
        resultMessage = "This request was approved in the Silo app.";
      } else if (isQuickConnect) {
        resultMessage = "That device is signed in.";
      } else {
        resultMessage = approvedHere ? "Your TV is signed in." : "That TV is already signed in.";
      }
      break;
    case "denied":
      resultMessage = remotePlayback ? "This request was declined." : "This sign-in was declined.";
      break;
    case "canceled":
      resultMessage = remotePlayback
        ? "The TV stopped waiting. Send playback from the Silo app again."
        : "This TV stopped waiting. Start again on the TV.";
      break;
    case "expired":
      if (remotePlayback) {
        resultMessage = "This request expired. Send playback from the Silo app again.";
      } else if (isQuickConnect) {
        resultMessage = "This code expired. Start Quick Connect again in the app.";
      } else {
        resultMessage = "This code expired. Your TV is showing a new one; scan it again.";
      }
      break;
  }

  return (
    <div className="auth-shell">
      <AuthBackground />
      <Card className="auth-card glass panel-border w-full max-w-md border-0">
        <CardHeader>
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">{serverName}</CardTitle>
          <p className="text-muted-foreground text-xs">{serverHost}</p>
          <CardDescription className="mt-2 text-sm leading-6">
            Sign in a TV or other device with your account.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          {!token && !code ? (
            <form onSubmit={handleCodeSubmit} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="device-code">Enter the code on your TV or app</Label>
                <Input
                  id="device-code"
                  value={codeInput}
                  onChange={(e) => setCodeInput(formatDeviceCode(e.target.value))}
                  inputMode="numeric"
                  autoCapitalize="characters"
                  autoComplete="one-time-code"
                  autoCorrect="off"
                  placeholder="1234 5678"
                />
              </div>
              <Button
                type="submit"
                className="w-full"
                disabled={!isCompleteDeviceCode(compactDeviceCode(codeInput))}
              >
                Continue
              </Button>
            </form>
          ) : loadingDetails || loading || setupLoading ? (
            <div className="text-muted-foreground text-sm">Looking up the code...</div>
          ) : needsAccount && !user ? (
            <div className="space-y-3">
              <p className="text-sm">
                Sign in to {serverName} to approve code {code} from your Jellyfin app.
              </p>
              <Button asChild className="w-full">
                <Link to={loginHref}>Sign in to approve</Link>
              </Button>
              <Button type="button" variant="outline" className="w-full" onClick={enterAnotherCode}>
                Enter another code
              </Button>
            </div>
          ) : loadError ? (
            <div className="space-y-4">
              <p className="text-sm" role="alert">
                {loadError === "not_found"
                  ? "We couldn't find that code. Check the code on your TV."
                  : loadError === "rate_limited"
                    ? tooManyCodesMessage(retryAfter)
                    : loadError === "impersonating"
                      ? impersonationText
                      : "Couldn't look up this code. Try again."}
              </p>
              {loadError === "failed" ? (
                <Button type="button" className="w-full" onClick={() => void loadDetails()}>
                  Try again
                </Button>
              ) : null}
              <Button type="button" variant="outline" className="w-full" onClick={enterAnotherCode}>
                Enter another code
              </Button>
            </div>
          ) : details ? (
            <div className="space-y-5">
              <div className="space-y-1">
                <h2 className="text-lg font-semibold">
                  {remotePlayback ? `Play on ${deviceName}?` : `Sign in ${deviceName}?`}
                </h2>
                {deviceLine ? <p className="text-muted-foreground text-sm">{deviceLine}</p> : null}
              </div>

              {details.status === "pending" && shownCode ? (
                <div className="border-border/60 bg-background/50 rounded-md border p-4">
                  <div className="text-muted-foreground text-sm">
                    Check that your {screen} shows
                  </div>
                  <div
                    className="mt-1 font-mono text-3xl font-semibold tracking-[0.12em]"
                    aria-hidden="true"
                  >
                    {shownCode}
                  </div>
                  <span className="sr-only">{spokenDeviceCode(shownCode)}</span>
                  {details.match_code && !isQuickConnect ? (
                    // TV apps released before user codes show only these
                    // words. Remove once both TV apps show the user code.
                    <div className="text-muted-foreground mt-2 text-xs">
                      Older TV apps show {details.match_code.toUpperCase()} instead.
                    </div>
                  ) : null}
                </div>
              ) : null}

              {details.status === "pending" ? (
                remotePlayback ? (
                  <div className="space-y-4">
                    <p className="text-sm">
                      This request comes from the Silo app that sent playback to this TV. Approve it
                      in that app.
                    </p>
                    <p className="text-muted-foreground text-sm">
                      The TV plays with the profile chosen in the app instead of signing in, so this
                      page can&apos;t approve it.
                    </p>
                    {/* No "Open in the Silo app": the apps' link opens their TV
                        sign-in, which can't approve a handoff. */}
                    {user && !isImpersonating ? (
                      <>
                        {actionError ? (
                          <p className="text-destructive text-sm" role="alert">
                            {actionError}
                          </p>
                        ) : null}
                        <Button
                          variant="outline"
                          className="w-full"
                          disabled={acting}
                          onClick={() => void handleDecision("deny")}
                        >
                          Not now
                        </Button>
                      </>
                    ) : null}
                  </div>
                ) : !user ? (
                  <div className="space-y-3">
                    <p className="text-sm">Sign in to {serverName} to approve this TV.</p>
                    {appLink ? (
                      <Button asChild className="w-full">
                        <a href={appLink}>
                          <Smartphone aria-hidden="true" /> Open in the Silo app
                        </a>
                      </Button>
                    ) : null}
                    <Button asChild className="w-full" variant={appLink ? "outline" : "default"}>
                      <Link to={loginHref}>Sign in to approve</Link>
                    </Button>
                  </div>
                ) : isImpersonating ? (
                  <p className="text-sm" role="status">
                    {impersonationText}
                  </p>
                ) : (
                  <div className="space-y-4">
                    <p className="text-sm">
                      You&apos;ll sign it in to {serverName} as{" "}
                      <span className="font-medium">{user.username}</span>.{" "}
                      <button
                        type="button"
                        className="text-primary underline"
                        onClick={switchAccount}
                      >
                        Not you? Switch account
                      </button>
                    </p>
                    {isQuickConnect ? null : (
                      <p className="text-muted-foreground text-sm">
                        Anyone using this TV can pick from your profiles. Profiles with a PIN stay
                        locked.
                      </p>
                    )}
                    <p className="text-sm font-medium">
                      Only approve a {isQuickConnect ? "device" : "TV"} that&apos;s in front of you
                      right now.
                    </p>
                    {details.ip_address_hint ? (
                      <p className="text-muted-foreground text-xs">
                        Requested from {details.ip_address_hint}
                      </p>
                    ) : null}
                    {actionError ? (
                      <p className="text-destructive text-sm" role="alert">
                        {actionError}
                      </p>
                    ) : null}
                    {isQuickConnect ? (
                      <QuickConnectApproval
                        code={details.user_code || code}
                        deviceName={deviceName}
                        activeProfileId={profile?.id ?? null}
                        busy={acting}
                        onApproved={quickConnectApproved}
                        onMoved={quickConnectMoved}
                        onDeny={() => void handleDecision("deny")}
                      />
                    ) : (
                      <div className="space-y-3">
                        <Button
                          className="w-full"
                          disabled={acting}
                          onClick={() => void handleDecision("approve")}
                        >
                          {acting ? "Signing in..." : "Sign in TV"}
                        </Button>
                        <Button
                          variant="outline"
                          className="w-full"
                          disabled={acting}
                          onClick={() => void handleDecision("deny")}
                        >
                          Not now
                        </Button>
                      </div>
                    )}
                  </div>
                )
              ) : null}

              {/* One live region stays mounted and only its text changes, so
                  the result of a decision is announced. */}
              <p
                ref={resultRef}
                tabIndex={-1}
                className={resultMessage ? "text-sm outline-none" : "sr-only"}
                role="status"
                aria-live="polite"
              >
                {resultMessage}
              </p>

              {!token ? (
                <Button
                  type="button"
                  variant="outline"
                  className="w-full"
                  onClick={enterAnotherCode}
                >
                  Enter another code
                </Button>
              ) : null}
            </div>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}
