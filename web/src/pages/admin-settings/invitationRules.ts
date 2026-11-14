import { useEffect, useRef } from "react";

import type { AdminInvitation as Invitation, InvitationDelivery } from "@/api/v2/invitations";
import type { MessageKey, Translate } from "@/i18n";

type InvitationStatus = Invitation["status"];

export const STATUS_BADGES: Record<
  InvitationStatus,
  { label: MessageKey; variant: "default" | "outline" }
> = {
  pending: { label: "web.admin.settings.invitations.status.pending", variant: "default" },
  accepted: { label: "web.admin.settings.invitations.status.accepted", variant: "outline" },
  expired: { label: "web.admin.settings.invitations.status.expired", variant: "outline" },
  revoked: { label: "web.admin.settings.invitations.status.revoked", variant: "outline" },
};

export function deliveryMessage(result: InvitationDelivery, t: Translate) {
  // A replaced link for an emailed invitation stays bound to its address.
  if (result.delivery_status === "not_requested" && result.invitation.email)
    return t("web.admin.settings.invitations.delivery.newLinkForEmail", {
      email: result.invitation.email,
    });
  if (result.delivery_status === "not_requested")
    return t("web.admin.settings.invitations.delivery.linkCreated");
  if (result.delivery_status === "sent")
    return t("web.admin.settings.invitations.delivery.sent", {
      email: String(result.invitation.email),
    });
  if (result.delivery_status === "not_configured")
    return t("web.admin.settings.invitations.delivery.notConfigured");
  return t("web.admin.settings.invitations.delivery.failed");
}
/** A pending link invitation has no address until someone accepts it. */
export function isLinkInvitation(invitation: Invitation) {
  return invitation.email === "";
}

export function useMounted() {
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return mounted;
}
