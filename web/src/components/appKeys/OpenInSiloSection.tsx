import { useState } from "react";
import { createSessionAppCode, type AppCode } from "@/api/v2/appCodes";
import { V2ProblemError } from "@/api/v2/request";
import { OpenSiloStep } from "@/components/appKeys/OpenSiloStep";
import { SignInAgainNotice } from "@/components/appKeys/SignInAgainNotice";
import { Button } from "@/components/ui/button";
import { useAppKeyCapability, useAppKeySessionAccess } from "@/hooks/queries/appKeySession";
import { useAuth } from "@/hooks/useAuth";
import { useT } from "@/i18n";

/**
 * "Open in Silo" in a person's own settings: an app code for this account, so
 * the Silo app on a phone or tablet enrolls its keys without a password.
 * Hidden where app keys or app codes are off, and for an administrator
 * acting as the account; a session that can't make a code because of how it
 * was opened gets SignInAgainNotice instead.
 */
export function OpenInSiloSection() {
  const t = useT();
  const { isImpersonating } = useAuth();
  const capability = useAppKeyCapability();
  const available = capability.data?.state === "available" && capability.data.app_codes;
  const access = useAppKeySessionAccess(Boolean(available) && !isImpersonating);
  const [appCode, setAppCode] = useState<AppCode | null>(null);
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);

  if (!available || isImpersonating || !access.data) return null;
  const { manage, sign_in_again: signInAgain } = access.data;

  async function issue() {
    setPending(true);
    setFailed(false);
    try {
      setAppCode(await createSessionAppCode());
    } catch (error) {
      // A 403 here means the answer above went stale (the profile changed,
      // say): ask again rather than guess why.
      if (error instanceof V2ProblemError && error.status === 403) void access.refetch();
      else setFailed(true);
    } finally {
      setPending(false);
    }
  }

  return (
    <section aria-labelledby="open-in-silo-title" className="mb-8 space-y-3">
      <h2 id="open-in-silo-title" className="text-lg font-semibold">
        {t("web.settings.openInSilo.title")}
      </h2>
      <p className="text-muted-foreground text-sm">{t("web.settings.openInSilo.description")}</p>
      {!manage ? (
        <SignInAgainNotice signInAgain={signInAgain} />
      ) : appCode ? (
        <OpenSiloStep appCode={appCode} onRenew={() => void issue()} renewing={pending} />
      ) : (
        <Button type="button" onClick={() => void issue()} disabled={pending}>
          {t("web.settings.openInSilo.action")}
        </Button>
      )}
      {failed && (
        <p role="alert" className="text-destructive text-sm">
          {t("web.appCode.failed")}
        </p>
      )}
    </section>
  );
}
