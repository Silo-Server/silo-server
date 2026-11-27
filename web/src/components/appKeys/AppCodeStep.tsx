import { useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Copy, Smartphone } from "lucide-react";
import QRCode from "react-qr-code";
import type { AppCode, JoinAppCode, LinkableJoinAppCode, SessionAppCode } from "@/api/v2/appCodes";
import { Button } from "@/components/ui/button";
import { copyTextToClipboard } from "@/lib/clipboard";
import { buildJoinLink } from "@/lib/joinLink";
import { scanCode as scanCodeFor, scanCodeLayout } from "@/lib/scanCode";
import { useT } from "@/i18n";

type StepProps<Code extends AppCode> = {
  appCode: Code;
  /** Gets a new code once this one expires; without it, expiredNote says what to do. */
  onRenew?: () => void;
  renewing?: boolean;
  /**
   * When the code expires, in milliseconds since the epoch: the time the
   * code was issued plus expires_in, kept by the caller so a step mounted
   * again never restarts the clock. Defaults to expires_in from mounting.
   */
  expiresAt?: number;
  /** What an expired code's step says when there is no renewal. */
  expiredNote?: string;
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
 * administrator's invitation): the scan code, the server address and the
 * code, to scan, type or paste in the Silo app to create the account. An app
 * that intercepted a link would hold the new administrator's session.
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
 * The step that shows a code for the signed-in account ("Open in Silo", and
 * the page that redeems a sign-in link): the scan code, the server address
 * and the code, to scan, type or paste in the Silo app. Never a link or a
 * scannable link (a URL in a QR code): a link can be received by an app other
 * than Silo, which would redeem the code first and get a key on this existing
 * account. The scan code is not such a link: it is text any QR reader can
 * read but only the Silo app acts on, and nothing opens it.
 */
export function SessionCodeStep({
  appCode,
  onRenew,
  renewing,
  expiresAt,
  expiredNote,
}: StepProps<SessionAppCode>) {
  const t = useT();
  return (
    <AppCodeStep
      key={appCode.code}
      appCode={appCode}
      onRenew={onRenew}
      renewing={renewing}
      expiresAt={expiresAt}
      expiredNote={expiredNote}
      title={t("web.settings.openInSilo.codeTitle")}
      instructions={t("web.settings.openInSilo.codeInstructions")}
    >
      <CodeToType appCode={appCode} />
    </AppCodeStep>
  );
}

/**
 * A code whose answer said link_allowed: false, for the person to move by
 * hand: the scan code (a QR code of text only the Silo app acts on,
 * never a link), the server address and the code as text to type, and Copy
 * code for a device that can't scan. A server address over 128 bytes has no
 * scan code, so the address and the code are all there is. The QR code grows
 * with the text so a module stays at least 3.5 px, inside a white margin of
 * four modules; where the column is narrower (the longest code on a phone)
 * the box stays centered and reaches into the card's padding rather than
 * shrinking.
 */
function CodeToType({ appCode }: { appCode: AppCode }) {
  const t = useT();
  const scanCode = scanCodeFor(appCode.code, appCode.server_address);
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">("idle");

  async function copyCode() {
    try {
      await copyTextToClipboard(appCode.code);
      setCopyState("copied");
    } catch {
      setCopyState("failed");
    }
  }

  return (
    <div className="space-y-3">
      {scanCode.text !== null ? (
        <div className="space-y-2">
          <ScanCodeImage text={scanCode.text} label={t("web.auth.appCode.scanCodeLabel")} />
          <p className="text-muted-foreground text-center text-xs">
            {t("web.auth.appCode.scanHint")}
          </p>
        </div>
      ) : (
        <p className="text-muted-foreground text-sm">
          {scanCode.reason === "too_long"
            ? t("web.auth.appCode.noScanCode")
            : t("web.auth.appCode.scanCodeUnavailable")}
        </p>
      )}
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
      <Button type="button" variant="outline" className="w-full" onClick={() => void copyCode()}>
        <Copy className="me-2 h-4 w-4" aria-hidden="true" />
        {t("web.auth.appCode.copyCode")}
      </Button>
      <p role="status" className="text-center text-sm empty:hidden">
        {copyState === "copied" && t("web.auth.appCode.codeCopied")}
      </p>
      {copyState === "failed" && (
        <p role="alert" className="text-destructive text-sm">
          {t("web.auth.appCode.copyFailed")}
        </p>
      )}
    </div>
  );
}

/**
 * The scan code's QR code in its white box: the margin is the quiet zone.
 * Centered; wider than a narrow column, it reaches into the card's padding.
 */
function ScanCodeImage({ text, label }: { text: string; label: string }) {
  const { pixels, margin } = scanCodeLayout(text);
  return (
    <div className="flex justify-center">
      <div className="shrink-0 rounded-md bg-white" style={{ padding: margin }}>
        <QRCode value={text} size={pixels} role="img" aria-label={label} />
      </div>
    </div>
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
  renewing = false,
  expiresAt,
  expiredNote,
  title,
  instructions,
  children,
}: StepProps<AppCode> & { title: string; instructions: string; children: ReactNode }) {
  const t = useT();
  const headingId = useId();
  const [deadline] = useState(() => expiresAt ?? Date.now() + appCode.expires_in * 1000);
  const [expired, setExpired] = useState(() => deadline <= Date.now());
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
    const timer = window.setTimeout(
      () => {
        focusRenew.current = section.current?.contains(document.activeElement) ?? false;
        setExpired(true);
      },
      Math.max(0, deadline - Date.now()),
    );
    return () => window.clearTimeout(timer);
  }, [deadline]);
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
          {onRenew ? (
            <Button
              ref={renew}
              type="button"
              className="w-full"
              onClick={onRenew}
              disabled={renewing}
            >
              {t("web.auth.appCode.renew")}
            </Button>
          ) : (
            expiredNote && <p className="text-muted-foreground text-sm">{expiredNote}</p>
          )}
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
