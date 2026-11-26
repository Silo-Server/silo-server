import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link, useBlocker } from "react-router";
import {
  redeemSignInLink,
  setSignInLinkPassword,
  type SignInLinkRedeemed,
} from "@/api/v2/appCodes";
import { V2ProblemError } from "@/api/v2/request";
import { SessionCodeStep } from "@/components/appKeys/AppCodeStep";
import { AuthCard, SignInHint } from "@/components/auth/AuthCard";
import { PasswordInput } from "@/components/PasswordInput";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { usePasswordPolicy } from "@/hooks/queries/account";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useLinkToken } from "@/hooks/useLinkToken";
import { linkTokenSpent, markLinkTokenSpent } from "@/lib/linkToken";
import { newPasswordHint, newPasswordProblem, passwordErrorMessage } from "@/lib/newPassword";
import { useT } from "@/i18n";

type Stage =
  | { kind: "intro" }
  | {
      kind: "redeemed";
      redeemed: SignInLinkRedeemed;
      view: "code" | "password";
      /** When the code expires: the redeem's answer time plus expires_in. */
      codeExpiresAt?: number;
    }
  | { kind: "passwordSet" }
  | { kind: "unavailable" }
  | { kind: "provider" };

/**
 * The page a sign-in link opens. A link works once and its app code lives
 * ten minutes, so nothing happens on load: the page says what will happen,
 * and redeems when the person presses Continue. The code is a session code
 * for an existing account, shown to type with the shared step and never put
 * in a link. The code and the set-password token live in memory only, so
 * while either is on screen the page says to keep it open and asks before
 * it is left; after a reload it says the link has been used.
 */
export default function SignInLink() {
  const token = useLinkToken();
  const t = useT();
  useDocumentTitle(t("web.auth.signInLink.documentTitle"));
  const [stage, setStage] = useState<Stage>(() =>
    token && !linkTokenSpent() ? { kind: "intro" } : { kind: "unavailable" },
  );
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  // A second press, or a second render's handler, must not redeem again:
  // the first redeem spent the link.
  const busy = useRef(false);
  // From Continue until the answer settles the link may already be spent, so
  // the page asks before it is left then too; after a handled failure it
  // holds nothing.
  const holding = stage.kind === "redeemed" || pending;
  useEffect(() => {
    if (!holding) return;
    const ask = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", ask);
    return () => window.removeEventListener("beforeunload", ask);
  }, [holding]);
  // Navigating inside the app (Back, Forward, a link) doesn't unload the
  // page, so the router asks the same question while a result is held.
  const leave = useBlocker(
    ({ currentLocation, nextLocation }) =>
      holding && currentLocation.pathname !== nextLocation.pathname,
  );

  async function redeem() {
    if (busy.current || stage.kind !== "intro") return;
    busy.current = true;
    setPending(true);
    setError("");
    try {
      const redeemed = await redeemSignInLink(token);
      markLinkTokenSpent();
      setStage({
        kind: "redeemed",
        redeemed,
        view: redeemed.appCode ? "code" : "password",
        codeExpiresAt: redeemed.appCode
          ? Date.now() + redeemed.appCode.expires_in * 1000
          : undefined,
      });
    } catch (err) {
      if (err instanceof V2ProblemError && err.status === 404) setStage({ kind: "unavailable" });
      else if (err instanceof V2ProblemError && err.problemType === "provider_sign_in_required")
        setStage({ kind: "provider" });
      else setError(t("web.auth.signInLink.failed"));
      // A refusal other than a spent link left it unspent: Continue may be
      // pressed again.
      busy.current = false;
    } finally {
      setPending(false);
    }
  }

  if (stage.kind === "unavailable") {
    return (
      <AuthCard
        title={t("web.auth.passwordReset.unavailableTitle")}
        description={t("web.auth.signInLink.unavailable")}
      >
        <SignInHint />
      </AuthCard>
    );
  }
  if (stage.kind === "provider") {
    return (
      <AuthCard
        title={t("web.auth.signInLink.title")}
        description={t("web.auth.signInLink.providerRequired")}
      >
        <Button asChild className="w-full">
          <Link to="/login">{t("web.auth.signIn")}</Link>
        </Button>
      </AuthCard>
    );
  }
  if (stage.kind === "passwordSet") {
    return (
      <AuthCard title={t("web.auth.passwordRules.passwordChanged")}>
        <p role="status" className="mb-4 text-sm">
          {t("web.auth.signInLink.passwordSet")}
        </p>
        <p className="text-muted-foreground mb-4 text-sm">
          {t("web.auth.signInLink.passwordSetLostDevices")}
        </p>
        <Button asChild className="w-full">
          <Link to="/login">{t("web.auth.signIn")}</Link>
        </Button>
      </AuthCard>
    );
  }
  if (stage.kind === "redeemed") {
    const { redeemed } = stage;
    if (stage.view === "password" && redeemed.setPasswordToken) {
      return (
        <>
          <SetPasswordForm
            token={redeemed.setPasswordToken}
            noCodeReason={redeemed.noCodeReason}
            onBack={redeemed.appCode ? () => setStage({ ...stage, view: "code" }) : undefined}
            onDone={() => setStage({ kind: "passwordSet" })}
            onUnavailable={() => setStage({ kind: "unavailable" })}
          />
          <LeaveDialog blocker={leave} />
        </>
      );
    }
    if (redeemed.appCode) {
      return (
        <AuthCard title={t("web.auth.signInLink.title")}>
          <KeepOpen />
          <SessionCodeStep
            appCode={redeemed.appCode}
            expiresAt={stage.codeExpiresAt}
            // The link is spent, so there is no new code to get; the password
            // route below stays while its token lives.
            expiredNote={t("web.auth.signInLink.codeExpired")}
          />
          {redeemed.setPasswordToken && (
            <Button
              type="button"
              variant="outline"
              className="mt-4 w-full"
              onClick={() => setStage({ ...stage, view: "password" })}
            >
              {t("web.auth.signInLink.setPasswordInstead")}
            </Button>
          )}
          <LeaveDialog blocker={leave} />
        </AuthCard>
      );
    }
  }
  return (
    <AuthCard title={t("web.auth.signInLink.title")} description={t("web.auth.signInLink.intro")}>
      {error && (
        <p role="alert" className="text-destructive mb-4 text-sm">
          {error}
        </p>
      )}
      <p className="text-muted-foreground mb-4 text-sm">{t("web.auth.signInLink.installFirst")}</p>
      <Button type="button" className="w-full" disabled={pending} onClick={() => void redeem()}>
        {t("web.ui.continue")}
      </Button>
      <SignInHint />
      <LeaveDialog blocker={leave} />
    </AuthCard>
  );
}

/**
 * Asks before an in-app navigation leaves the page while it holds what the
 * spent link handed out; staying keeps it, leaving lets the navigation go.
 */
function LeaveDialog({ blocker }: { blocker: ReturnType<typeof useBlocker> }) {
  const t = useT();
  const proceeding = useRef(false);
  useEffect(() => {
    if (blocker.state !== "blocked") proceeding.current = false;
  }, [blocker.state]);
  return (
    <AlertDialog
      open={blocker.state === "blocked"}
      onOpenChange={(open) => {
        if (open || proceeding.current) return;
        blocker.reset?.();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("web.auth.signInLink.leaveTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("web.auth.signInLink.leaveDescription")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={() => {
              proceeding.current = true;
              blocker.proceed?.();
            }}
          >
            {t("web.auth.signInLink.leaveAction")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Says to keep the page open while it holds what the spent link handed out. */
function KeepOpen() {
  const t = useT();
  return <p className="mb-4 text-sm font-medium">{t("web.auth.signInLink.keepOpen")}</p>;
}

/** Chooses a password with the token the redeem handed out, once. */
function SetPasswordForm({
  token,
  noCodeReason,
  onBack,
  onDone,
  onUnavailable,
}: {
  token: string;
  /** Why the link gave no code, said above the form. */
  noCodeReason: SignInLinkRedeemed["noCodeReason"];
  onBack?: () => void;
  onDone: () => void;
  onUnavailable: () => void;
}) {
  const t = useT();
  const policy = usePasswordPolicy();
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const busy = useRef(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy.current) return;
    const problem = newPasswordProblem(password, confirmation, policy);
    if (problem) {
      setError(problem);
      return;
    }
    busy.current = true;
    setSubmitting(true);
    setError("");
    try {
      await setSignInLinkPassword(token, password);
      onDone();
    } catch (err) {
      if (err instanceof V2ProblemError && err.status === 404) onUnavailable();
      else setError(passwordErrorMessage(err, t("web.auth.passwordReset.setFailed")));
    } finally {
      busy.current = false;
      setSubmitting(false);
    }
  }

  return (
    <AuthCard
      title={t("web.auth.signInLink.passwordTitle")}
      description={t("web.auth.signInLink.passwordDescription")}
    >
      <KeepOpen />
      {noCodeReason && (
        <p className="mb-4 text-sm">
          {noCodeReason === "administrator"
            ? t("web.auth.signInLink.noCodeAdministrator")
            : t("web.auth.signInLink.noCodeAppKeysOff")}
        </p>
      )}
      {error && (
        <p role="alert" className="text-destructive mb-4 text-sm">
          {error}
        </p>
      )}
      <form onSubmit={submit} className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="sign-in-link-password">{t("web.auth.passwordRules.newPassword")}</Label>
          <p className="text-muted-foreground text-xs">{newPasswordHint(policy)}</p>
          <PasswordInput
            id="sign-in-link-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={submitting}
            autoComplete="new-password"
            autoFocus
            required
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="sign-in-link-confirm">
            {t("web.auth.passwordRules.confirmNewPassword")}
          </Label>
          <PasswordInput
            id="sign-in-link-confirm"
            value={confirmation}
            onChange={(e) => setConfirmation(e.target.value)}
            disabled={submitting}
            autoComplete="new-password"
            required
          />
        </div>
        <Button type="submit" className="w-full" disabled={submitting}>
          {submitting ? t("web.ui.saving") : t("web.auth.passwordRules.savePassword")}
        </Button>
        {onBack && (
          <Button
            type="button"
            variant="ghost"
            className="w-full"
            disabled={submitting}
            onClick={onBack}
          >
            {t("web.auth.signInLink.backToCode")}
          </Button>
        )}
      </form>
    </AuthCard>
  );
}
