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
import { useT } from "@/i18n";

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
    case "roku":
      return "Roku";
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
  const t = useT();

  useDocumentTitle(t("web.auth.activate.documentTitle"));

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
    const failure = t(
      action === "approve" ? "web.auth.activate.approveFailed" : "web.auth.activate.declineFailed",
    );
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
  const impersonationText = user?.username
    ? t("web.auth.activate.impersonating", { username: user.username })
    : t("web.auth.activate.impersonatingUnknown");
  // A tapped link into the phone app, which approves with its own session.
  // Offered only where a Silo app exists, and never opened automatically.
  const appLink =
    details && detectMobilePlatform(navigator.userAgent)
      ? buildDeviceDeepLink(details.server_id, window.location.origin, details.user_code || code)
      : null;
  const serverHost = window.location.host;
  // Empty when the device sent no name; each sentence then says "this device".
  const deviceName = details?.device_name || "";
  const shownCode = displayDeviceCode(details?.user_code || code);
  const requestedAgo = formatRelativeTime(details?.requested_at);
  const deviceLine = [
    details?.device_platform ? platformLabel(details.device_platform) : "",
    requestedAgo ? t("web.auth.activate.requestedAgo", { time: requestedAgo }) : "",
  ]
    .filter(Boolean)
    .join(" · ");

  let resultMessage = "";
  switch (details?.status) {
    case "approved":
      if (remotePlayback) {
        resultMessage = t("web.auth.activate.result.remoteApproved");
      } else if (isQuickConnect) {
        if (!approvedProfile) {
          resultMessage = t("web.auth.activate.result.quickConnectApproved");
        } else if (deviceName) {
          resultMessage = t("web.auth.activate.result.quickConnectDone", {
            device: deviceName,
            profile: approvedProfile,
          });
        } else {
          resultMessage = t("web.auth.activate.result.quickConnectDoneUnknown", {
            profile: approvedProfile,
          });
        }
      } else {
        resultMessage = t(
          approvedHere ? "web.auth.activate.result.tvDone" : "web.auth.activate.result.tvApproved",
        );
      }
      break;
    case "consumed":
      if (remotePlayback) {
        resultMessage = t("web.auth.activate.result.remoteApproved");
      } else if (isQuickConnect) {
        resultMessage = t("web.auth.activate.result.quickConnectSignedIn");
      } else {
        resultMessage = t(
          approvedHere
            ? "web.auth.activate.result.tvSignedIn"
            : "web.auth.activate.result.tvAlreadySignedIn",
        );
      }
      break;
    case "denied":
      resultMessage = t(
        remotePlayback
          ? "web.auth.activate.result.remoteDeclined"
          : "web.auth.activate.result.declined",
      );
      break;
    case "canceled":
      resultMessage = t(
        remotePlayback
          ? "web.auth.activate.result.remoteCanceled"
          : "web.auth.activate.result.canceled",
      );
      break;
    case "expired":
      if (remotePlayback) {
        resultMessage = t("web.auth.activate.result.remoteExpired");
      } else if (isQuickConnect) {
        resultMessage = t("web.auth.activate.result.quickConnectExpired");
      } else {
        resultMessage = t("web.auth.activate.result.expired");
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
            {t("web.auth.activate.description")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          {!token && !code ? (
            <form onSubmit={handleCodeSubmit} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="device-code">{t("web.auth.activate.codeLabel")}</Label>
                <Input
                  id="device-code"
                  value={codeInput}
                  onChange={(e) => setCodeInput(formatDeviceCode(e.target.value))}
                  inputMode="numeric"
                  autoCapitalize="characters"
                  autoComplete="one-time-code"
                  autoCorrect="off"
                  // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- example code digits, not language text
                  placeholder="1234 5678"
                />
              </div>
              <Button
                type="submit"
                className="w-full"
                disabled={!isCompleteDeviceCode(compactDeviceCode(codeInput))}
              >
                {t("web.ui.continue")}
              </Button>
            </form>
          ) : loadingDetails || loading || setupLoading ? (
            <div className="text-muted-foreground text-sm">{t("web.auth.activate.lookingUp")}</div>
          ) : needsAccount && !user ? (
            <div className="space-y-3">
              <p className="text-sm">
                {t("web.auth.activate.quickConnectSignIn", { server: serverName, code })}
              </p>
              <Button asChild className="w-full">
                <Link to={loginHref}>{t("web.auth.activate.signInToApprove")}</Link>
              </Button>
              <Button type="button" variant="outline" className="w-full" onClick={enterAnotherCode}>
                {t("web.auth.activate.enterAnotherCode")}
              </Button>
            </div>
          ) : loadError ? (
            <div className="space-y-4">
              <p className="text-sm" role="alert">
                {loadError === "not_found"
                  ? t("web.auth.activate.notFound")
                  : loadError === "rate_limited"
                    ? tooManyCodesMessage(retryAfter)
                    : loadError === "impersonating"
                      ? impersonationText
                      : t("web.auth.activate.lookupFailed")}
              </p>
              {loadError === "failed" ? (
                <Button type="button" className="w-full" onClick={() => void loadDetails()}>
                  {t("common.tryAgain")}
                </Button>
              ) : null}
              <Button type="button" variant="outline" className="w-full" onClick={enterAnotherCode}>
                {t("web.auth.activate.enterAnotherCode")}
              </Button>
            </div>
          ) : details ? (
            <div className="space-y-5">
              <div className="space-y-1">
                {/* An expired request has nothing left to approve, so it
                    drops the question. */}
                {details.status === "expired" ? null : (
                  <h2 className="text-lg font-semibold">
                    {remotePlayback
                      ? deviceName
                        ? t("web.auth.activate.playOn", { device: deviceName })
                        : t("web.auth.activate.playOnUnknown")
                      : deviceName
                        ? t("web.auth.activate.signInDevice", { device: deviceName })
                        : t("web.auth.activate.signInDeviceUnknown")}
                  </h2>
                )}
                {deviceLine ? <p className="text-muted-foreground text-sm">{deviceLine}</p> : null}
              </div>

              {details.status === "pending" && shownCode ? (
                <div className="border-border/60 bg-background/50 rounded-md border p-4">
                  <div className="text-muted-foreground text-sm">
                    {t(isQuickConnect ? "web.auth.activate.checkApp" : "web.auth.activate.checkTv")}
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
                      {t("web.auth.activate.olderTvApps", {
                        words: details.match_code.toUpperCase(),
                      })}
                    </div>
                  ) : null}
                </div>
              ) : null}

              {details.status === "pending" ? (
                remotePlayback ? (
                  <div className="space-y-4">
                    <p className="text-sm">{t("web.auth.activate.remotePlaybackApproveInApp")}</p>
                    <p className="text-muted-foreground text-sm">
                      {t("web.auth.activate.remotePlaybackCantApprove")}
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
                          {t("web.auth.activate.notNow")}
                        </Button>
                      </>
                    ) : null}
                  </div>
                ) : !user ? (
                  <div className="space-y-3">
                    <p className="text-sm">
                      {t("web.auth.activate.signInToApproveTv", { server: serverName })}
                    </p>
                    {appLink ? (
                      <Button asChild className="w-full">
                        <a href={appLink}>
                          <Smartphone aria-hidden="true" /> {t("web.auth.openInApp")}
                        </a>
                      </Button>
                    ) : null}
                    <Button asChild className="w-full" variant={appLink ? "outline" : "default"}>
                      <Link to={loginHref}>{t("web.auth.activate.signInToApprove")}</Link>
                    </Button>
                  </div>
                ) : isImpersonating ? (
                  <p className="text-sm" role="status">
                    {impersonationText}
                  </p>
                ) : (
                  <div className="space-y-4">
                    <p className="text-sm">
                      {t("web.auth.activate.signInAs", {
                        server: serverName,
                        username: user.username,
                        name: (chunks) => <span className="font-medium">{chunks}</span>,
                        switch: (chunks) => (
                          <button
                            type="button"
                            className="text-primary underline"
                            onClick={switchAccount}
                          >
                            {chunks}
                          </button>
                        ),
                      })}
                    </p>
                    {isQuickConnect ? null : (
                      <p className="text-muted-foreground text-sm">
                        {t("web.auth.activate.profilesShared")}
                      </p>
                    )}
                    <p className="text-sm font-medium">
                      {t(
                        isQuickConnect
                          ? "web.auth.activate.onlyApproveDevice"
                          : "web.auth.activate.onlyApproveTv",
                      )}
                    </p>
                    {details.ip_address_hint ? (
                      <p className="text-muted-foreground text-xs">
                        {t("web.auth.activate.requestedFrom", { ip: details.ip_address_hint })}
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
                          {acting ? t("web.auth.login.signingIn") : t("web.auth.activate.signInTv")}
                        </Button>
                        <Button
                          variant="outline"
                          className="w-full"
                          disabled={acting}
                          onClick={() => void handleDecision("deny")}
                        >
                          {t("web.auth.activate.notNow")}
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

              {/* A link carries its request in the token, so it offers manual
                  entry only once that request has expired. */}
              {!token || details.status === "expired" ? (
                <Button
                  type="button"
                  variant="outline"
                  className="w-full"
                  onClick={enterAnotherCode}
                >
                  {t("web.auth.activate.enterAnotherCode")}
                </Button>
              ) : null}
            </div>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}
