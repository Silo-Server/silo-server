import { Copy, Link2, Mail, MailX } from "lucide-react";

import type { AdminInvitation as Invitation } from "@/api/v2/invitations";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

import { TOUCH_FIELD_CLASS } from "./touchClasses";

/** Shown in the Delivery column when the delivery is unknown. */
const NO_DELIVERY = "—";

// The claim-link box shown after create/resend. min-w-0 + overflow-hidden on
// every level matters: the URL is one unbreakable token, and without them it
// forces the dialog wider than the viewport on phones. Below lg the URL wraps
// so the whole link stays readable; from lg up it truncates.
export function ClaimLinkBox({
  claimUrl,
  finePrint,
  onCopy,
  onDone,
}: {
  claimUrl: string;
  finePrint: string;
  onCopy: (text: string) => Promise<void>;
  onDone: () => void;
}) {
  const t = useT();
  return (
    <div className="min-w-0 space-y-4">
      <div className="bg-muted min-w-0 overflow-hidden rounded-md p-2.5">
        <code className="block truncate text-xs max-lg:break-all max-lg:whitespace-normal">
          {claimUrl}
        </code>
      </div>
      <p className="text-muted-foreground text-xs">{finePrint}</p>
      <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <Button variant="outline" className={TOUCH_FIELD_CLASS} onClick={() => onCopy(claimUrl)}>
          <Copy className="me-1.5 h-4 w-4" /> {t("web.admin.settings.invitations.claimLink.copy")}
        </Button>
        <Button className={TOUCH_FIELD_CLASS} onClick={onDone}>
          {t("common.done")}
        </Button>
      </div>
    </div>
  );
}

export function DeliveryLabel({ delivery }: { delivery: Invitation["delivery"] }) {
  const t = useT();
  if (delivery === "link") {
    const label = t("web.admin.settings.invitations.deliveryLabel.link");
    return (
      <span className="inline-flex items-center gap-1.5 text-sm">
        <Link2 className="h-3.5 w-3.5" aria-hidden="true" /> {label}
      </span>
    );
  }
  if (delivery === "email_sent") {
    const label = t("web.admin.settings.invitations.deliveryLabel.emailSent");
    return (
      <span className="inline-flex items-center gap-1.5 text-sm">
        <Mail className="h-3.5 w-3.5" aria-hidden="true" /> {label}
      </span>
    );
  }
  if (delivery === "email_unconfirmed") {
    const unconfirmed = t("web.admin.settings.invitations.deliveryLabel.emailUnconfirmed");
    return (
      <span
        className="inline-flex items-center gap-1.5 text-sm text-amber-500"
        title={t("web.admin.settings.invitations.deliveryLabel.emailUnconfirmedTitle")}
      >
        <MailX className="h-3.5 w-3.5" aria-hidden="true" /> {unconfirmed}
      </span>
    );
  }
  return (
    <span
      className="text-muted-foreground text-sm"
      title={t("web.admin.settings.invitations.deliveryLabel.unknownTitle")}
    >
      {NO_DELIVERY}
    </span>
  );
}
