import { useEffect, useState } from "react";
import { Smartphone } from "lucide-react";
import type { AppCode } from "@/api/v2/appCodes";
import { Button } from "@/components/ui/button";
import { buildJoinLink } from "@/lib/joinLink";
import { useT } from "@/i18n";

/**
 * The "Open Silo" step: a button that hands the Silo app this server's
 * address and a one-time app code. The person may have to install the app
 * first, so the step stays until the code expires, and then offers a new
 * one. Nothing fires automatically: the page can't tell whether the app is
 * installed, and a custom-scheme link only works reliably when tapped.
 */
export function OpenSiloStep({
  appCode,
  onRenew,
  renewing,
}: {
  appCode: AppCode;
  onRenew: () => void;
  renewing: boolean;
}) {
  // A new code remounts the step (keyed by the caller), so expiry restarts.
  return (
    <OpenSiloStepFor key={appCode.code} appCode={appCode} onRenew={onRenew} renewing={renewing} />
  );
}

function OpenSiloStepFor({
  appCode,
  onRenew,
  renewing,
}: {
  appCode: AppCode;
  onRenew: () => void;
  renewing: boolean;
}) {
  const t = useT();
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    const timer = window.setTimeout(() => setExpired(true), appCode.expires_in * 1000);
    return () => window.clearTimeout(timer);
  }, [appCode.expires_in]);

  return (
    <section aria-labelledby="open-silo-title" className="space-y-3">
      <h2 id="open-silo-title" className="text-lg font-semibold">
        {t("web.auth.appCode.title")}
      </h2>
      <p className="text-muted-foreground text-sm leading-6">
        {t("web.auth.appCode.instructions")}
      </p>
      {expired ? (
        <div role="status" className="space-y-3">
          <p>{t("web.auth.appCode.expired")}</p>
          <Button type="button" className="w-full" onClick={onRenew} disabled={renewing}>
            {t("web.auth.appCode.renew")}
          </Button>
        </div>
      ) : (
        <>
          <Button asChild size="lg" className="h-12 w-full text-base font-semibold">
            <a href={buildJoinLink(appCode.server_address, appCode.code, "silo")}>
              <Smartphone className="me-2 h-5 w-5" /> {t("web.auth.appCode.open")}
            </a>
          </Button>
          <p className="text-center text-sm">
            <a
              className="text-foreground underline hover:no-underline"
              href={buildJoinLink(appCode.server_address, appCode.code, "https")}
            >
              {t("web.auth.appCode.webLink")}
            </a>
          </p>
          <p className="text-muted-foreground text-center text-xs">
            {t("web.auth.appCode.singleUse")}
          </p>
        </>
      )}
    </section>
  );
}
