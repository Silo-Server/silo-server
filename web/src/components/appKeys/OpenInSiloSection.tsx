import { useState } from "react";
import { createSessionAppCode, type AppCode } from "@/api/v2/appCodes";
import { V2ProblemError } from "@/api/v2/request";
import { OpenSiloStep } from "@/components/appKeys/OpenSiloStep";
import { SignInAgainNotice } from "@/components/appKeys/SignInAgainNotice";
import { ProfilePinDialog } from "@/components/profiles/ProfilePinDialog";
import { Button } from "@/components/ui/button";
import { useAppKeyCapability, useAppKeySessionAccess } from "@/hooks/queries/appKeySession";
import { useAuth } from "@/hooks/useAuth";
import { useT } from "@/i18n";

/**
 * "Open in Silo" in a person's own settings: an app code for this account, so
 * the Silo app on a phone or tablet enrolls its keys without a password.
 * Hidden where app keys or app codes are off, and for an administrator
 * acting as the account; a session that can't make a code because of how it
 * was opened gets SignInAgainNotice instead. Only the household's main
 * profile makes a code: a PIN it hasn't proven is asked for, as Your Data
 * asks for it, and the request repeated.
 */
export function OpenInSiloSection() {
  const t = useT();
  const { isImpersonating, profile, selectProfile, verifyProfilePin } = useAuth();
  const capability = useAppKeyCapability();
  const available = capability.data?.state === "available" && capability.data.app_codes;
  const access = useAppKeySessionAccess(Boolean(available) && !isImpersonating);
  const [appCode, setAppCode] = useState<AppCode | null>(null);
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);
  const [askPin, setAskPin] = useState(false);

  if (!available || isImpersonating || access.isPending) return null;

  async function issue() {
    setPending(true);
    setFailed(null);
    try {
      setAppCode(await createSessionAppCode());
    } catch (error) {
      if (!(error instanceof V2ProblemError)) setFailed(t("web.auth.appCode.failed"));
      else if (error.problemType === "profile_verification_required") setAskPin(true);
      else if (error.problemType === "main_profile_required")
        setFailed(t("web.settings.openInSilo.mainProfileOnly"));
      // The session answer went stale: ask again rather than guess why.
      else if (error.problemType === "permission_denied") void access.refetch();
      else setFailed(t("web.auth.appCode.failed"));
    } finally {
      setPending(false);
    }
  }

  let body;
  if (!access.data) {
    // Reading what this session may do failed: say so, rather than look as
    // if the server had no Silo app support.
    body = (
      <div role="alert" className="space-y-3">
        <p className="text-sm">{t("web.settings.openInSilo.loadFailed")}</p>
        <Button type="button" variant="outline" onClick={() => void access.refetch()}>
          {t("common.retry")}
        </Button>
      </div>
    );
  } else if (!access.data.manage) {
    body = <SignInAgainNotice signInAgain={access.data.sign_in_again} />;
  } else if (appCode) {
    body = <OpenSiloStep appCode={appCode} onRenew={() => void issue()} renewing={pending} />;
  } else {
    body = (
      <Button type="button" onClick={() => void issue()} disabled={pending}>
        {t("web.settings.openInSilo.action")}
      </Button>
    );
  }

  return (
    <section aria-labelledby="open-in-silo-title" className="mb-8 space-y-3">
      <h2 id="open-in-silo-title" className="text-lg font-semibold">
        {t("web.settings.openInSilo.title")}
      </h2>
      <p className="text-muted-foreground text-sm">{t("web.settings.openInSilo.description")}</p>
      {body}
      {failed && (
        <p role="alert" className="text-destructive text-sm">
          {failed}
        </p>
      )}
      <ProfilePinDialog
        profile={askPin ? profile : null}
        description={t("web.settings.openInSilo.pinDescription")}
        onClose={() => setAskPin(false)}
        onVerified={(verified, token) => {
          setAskPin(false);
          selectProfile(verified, token);
          void issue();
        }}
        verifyPin={verifyProfilePin}
      />
    </section>
  );
}
