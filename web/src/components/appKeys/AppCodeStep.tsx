import { useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Smartphone } from "lucide-react";
import type { AppCode, JoinAppCode, LinkableJoinAppCode, SessionAppCode } from "@/api/v2/appCodes";
import { Button } from "@/components/ui/button";
import { buildJoinLink } from "@/lib/joinLink";
import { useT } from "@/i18n";

type StepProps<Code extends AppCode> = {
  appCode: Code;
  onRenew: () => void;
  renewing: boolean;
};

/**
 * The step that hands the Silo app an invitation's code whose answer allowed
 * a link (linkableJoinCode): a button that opens
 * the app (silo://join) and the same fragment after https://siloserver.org/join.
 * The person may have to install the app first, so the step stays until the
 * code expires, and then offers a new one. Nothing fires automatically: the
 * page can't tell whether the app is installed, and a custom-scheme link only
 * works reliably when tapped. Only a join code the server allows in a link
 * (link_allowed) goes in one, which an administrator's invitation is not: an
 * app that intercepts it gets a new, empty account with no administrative
 * power, which the invited person notices.
 */
export function JoinLinkStep({ appCode, onRenew, renewing }: StepProps<LinkableJoinAppCode>) {
  const t = useT();
  return (
    <AppCodeStep
      key={appCode.code}
      appCode={appCode}
      onRenew={onRenew}
      renewing={renewing}
      title={t("web.auth.appCode.title")}
      instructions={t("web.auth.appCode.instructions")}
    >
      <Button asChild size="lg" className="h-12 w-full text-base font-semibold">
        <a href={buildJoinLink(appCode, "silo")}>
          <Smartphone className="me-2 h-5 w-5" /> {t("web.auth.appCode.open")}
        </a>
      </Button>
      <p className="text-center text-sm">
        <a
          className="text-foreground underline hover:no-underline"
          href={buildJoinLink(appCode, "https")}
        >
          {t("web.auth.appCode.webLink")}
        </a>
      </p>
    </AppCodeStep>
  );
}

/**
 * The step for an invitation's code whose answer refused a link (an
 * administrator's invitation): the server address and the code, to type in
 * the Silo app to create the account. An app that intercepted a link would
 * hold the new administrator's session.
 */
export function JoinCodeStep({ appCode, onRenew, renewing }: StepProps<JoinAppCode>) {
  const t = useT();
  return (
    <AppCodeStep
      key={appCode.code}
      appCode={appCode}
      onRenew={onRenew}
      renewing={renewing}
      title={t("web.auth.appCode.typeTitle")}
      instructions={t("web.auth.appCode.typeInstructions")}
    >
      <CodeToType appCode={appCode} />
    </AppCodeStep>
  );
}

/**
 * The step that shows a code for the signed-in account ("Open in Silo"): the
 * server address and the code, to type in the Silo app. Never a link or a
 * scannable link: a link can be received by an app other than Silo, which
 * would redeem the code first and get a key on this existing account.
 */
export function SessionCodeStep({ appCode, onRenew, renewing }: StepProps<SessionAppCode>) {
  const t = useT();
  return (
    <AppCodeStep
      key={appCode.code}
      appCode={appCode}
      onRenew={onRenew}
      renewing={renewing}
      title={t("web.settings.openInSilo.codeTitle")}
      instructions={t("web.settings.openInSilo.codeInstructions")}
    >
      <CodeToType appCode={appCode} />
    </AppCodeStep>
  );
}

/** The server address and the code as text to type: never a link. */
function CodeToType({ appCode }: { appCode: AppCode }) {
  const t = useT();
  return (
    <dl className="space-y-2">
      <div>
        <dt className="text-muted-foreground text-xs">{t("web.auth.appCode.serverAddress")}</dt>
        <dd className="font-mono text-sm break-all select-all">{appCode.server_address}</dd>
      </div>
      <div>
        <dt className="text-muted-foreground text-xs">{t("web.auth.appCode.code")}</dt>
        <dd className="font-mono text-base break-all select-all">{appCode.code}</dd>
      </div>
    </dl>
  );
}

/**
 * What both steps share: the heading, which takes focus when the step
 * appears; the instructions; and expiry, after which the content gives way
 * to a renewal button. It renders no link of its own: what the code is shown
 * as is the caller's, by kind.
 */
function AppCodeStep({
  appCode,
  onRenew,
  renewing,
  title,
  instructions,
  children,
}: StepProps<AppCode> & { title: string; instructions: string; children: ReactNode }) {
  const t = useT();
  const headingId = useId();
  const [expired, setExpired] = useState(false);
  const section = useRef<HTMLElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const renew = useRef<HTMLButtonElement>(null);
  // The control that led here is gone (or disabled), so the new step's
  // heading takes focus, as a screen reader then announces the step.
  useEffect(() => heading.current?.focus(), []);
  // Expiry replaces the step's content; whoever was on it moves to the
  // renewal button rather than falling back to the page.
  const focusRenew = useRef(false);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      focusRenew.current = section.current?.contains(document.activeElement) ?? false;
      setExpired(true);
    }, appCode.expires_in * 1000);
    return () => window.clearTimeout(timer);
  }, [appCode.expires_in]);
  useEffect(() => {
    if (expired && focusRenew.current) renew.current?.focus();
  }, [expired]);
  // A renewal that was refused leaves the step as it was; the button was
  // disabled while it ran, so focus that fell to the page comes back to it.
  useEffect(() => {
    if (!renewing && expired && document.activeElement === document.body) renew.current?.focus();
  }, [renewing, expired]);

  return (
    <section ref={section} aria-labelledby={headingId} className="space-y-3">
      <h2 id={headingId} ref={heading} tabIndex={-1} className="text-lg font-semibold outline-none">
        {title}
      </h2>
      <p className="text-muted-foreground text-sm leading-6">{instructions}</p>
      {expired ? (
        <div role="status" className="space-y-3">
          <p>{t("web.auth.appCode.expired")}</p>
          <Button
            ref={renew}
            type="button"
            className="w-full"
            onClick={onRenew}
            disabled={renewing}
          >
            {t("web.auth.appCode.renew")}
          </Button>
        </div>
      ) : (
        <>
          {children}
          <p className="text-muted-foreground text-center text-xs">
            {t("web.auth.appCode.singleUse")}
          </p>
        </>
      )}
    </section>
  );
}
