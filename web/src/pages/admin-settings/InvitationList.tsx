import { Ellipsis, Link2, MailPlus, RotateCw, Trash2, type LucideIcon } from "lucide-react";

import type { AdminInvitation as Invitation, InvitationDeliveryChoice } from "@/api/v2/invitations";
import {
  CARD_ACTION_CLASS,
  CardField,
  CardFields,
  DiagnosticsCard,
  DiagnosticsCardList,
} from "@/components/admin/libraries/DiagnosticsListParts";
import { useLibrariesTableLayout } from "@/components/admin/libraries/diagnosticsListState";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useT } from "@/i18n";
import { formatDate } from "@/lib/datetime";
import { cn } from "@/lib/utils";

import { DeliveryLabel } from "./InvitationDisplay";
import { STATUS_BADGES, isLinkInvitation } from "./invitationRules";
import { MENU_ITEM_CLASS } from "./touchClasses";

/** What the invitations list needs from the tab, shared by every row. */
export interface InvitationListProps {
  invitations: Invitation[];
  emailDelivery: boolean;
  viewerIsOwner: boolean;
  /** A resend or revoke is running or failed; row actions wait for it. */
  resending: boolean;
  /** A fresh link needs the public URL; revoking one does not. */
  resendBlocked: boolean;
  onResend: (invitation: Invitation, delivery: InvitationDeliveryChoice) => void;
  onRevoke: (invitation: Invitation) => void;
}

interface InvitationAction {
  key: string;
  label: string;
  icon: LucideIcon;
  disabled: boolean;
  destructive: boolean;
  run: () => void;
}

/**
 * Everything one invitation row shows, derived once for the table row and the
 * card: the recipient line, its details, the status badge, and the actions the
 * invitation's status allows.
 */
function useInvitationRow(
  invitation: Invitation,
  {
    emailDelivery,
    viewerIsOwner,
    resending,
    resendBlocked,
    onResend,
    onRevoke,
  }: Omit<InvitationListProps, "invitations">,
) {
  const t = useT();
  // Resending re-grants the role, so only the Owner resends an admin invitation.
  const resendAllowed = invitation.role !== "admin" || viewerIsOwner;
  const showResend =
    resendAllowed && (invitation.status === "pending" || invitation.status === "expired");
  const showRevoke = invitation.status === "pending";
  const link = isLinkInvitation(invitation);
  // A link invitation's note is how the admin told it apart, so it stays
  // after acceptance fills in the address.
  const shared = invitation.delivery === "link";
  const details = [
    !link && invitation.status === "accepted" && shared
      ? t("web.admin.settings.invitations.row.joinedByLink")
      : "",
    shared && invitation.note
      ? t("web.admin.settings.invitations.row.note", { note: invitation.note })
      : "",
    invitation.invited_by_name
      ? t("web.admin.settings.invitations.row.invitedBy", { name: invitation.invited_by_name })
      : "",
  ].filter(Boolean);

  const actions: InvitationAction[] = [];
  if (showResend) {
    // Replacing the link never emails; an emailed invitation keeps its address.
    actions.push({
      key: "link",
      label: link
        ? t("web.admin.settings.invitations.row.replaceLink")
        : t("web.admin.settings.invitations.row.copyNewLink"),
      icon: link ? RotateCw : Link2,
      disabled: resending || resendBlocked,
      destructive: false,
      run: () => onResend(invitation, "link"),
    });
  }
  if (showResend && !link && emailDelivery) {
    actions.push({
      key: "email",
      label: t("web.admin.settings.invitations.row.emailNewLink"),
      icon: MailPlus,
      disabled: resending || resendBlocked,
      destructive: false,
      run: () => onResend(invitation, "email"),
    });
  }
  if (showRevoke) {
    actions.push({
      key: "revoke",
      label: t("web.admin.settings.invitations.row.revokeLink"),
      icon: Trash2,
      disabled: resending,
      destructive: true,
      run: () => onRevoke(invitation),
    });
  }

  return {
    link,
    recipient: link ? t("web.admin.settings.invitations.row.linkInvitation") : invitation.email,
    details: details.join(" · "),
    badge: STATUS_BADGES[invitation.status],
    expires:
      invitation.status === "pending"
        ? t("web.admin.settings.invitations.row.expires", {
            date: formatDate(invitation.expires_at),
          })
        : "",
    created: formatDate(invitation.created_at),
    actions,
  };
}

/** The invitation history: a table from `lg` up, cards below it. */
export function InvitationList({ invitations, ...shared }: InvitationListProps) {
  const t = useT();
  const showTable = useLibrariesTableLayout();
  if (!showTable) {
    return (
      <DiagnosticsCardList>
        {invitations.map((inv) => (
          <InvitationCard key={inv.id} invitation={inv} shared={shared} />
        ))}
      </DiagnosticsCardList>
    );
  }
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t("web.admin.settings.invitations.table.recipient")}</TableHead>
          <TableHead>{t("web.admin.settings.invitations.table.role")}</TableHead>
          <TableHead>{t("web.admin.settings.invitations.table.status")}</TableHead>
          <TableHead>{t("web.admin.settings.invitations.table.delivery")}</TableHead>
          <TableHead>{t("web.admin.settings.invitations.table.created")}</TableHead>
          <TableHead className="w-32" />
        </TableRow>
      </TableHeader>
      <TableBody>
        {invitations.map((inv) => (
          <InvitationRow key={inv.id} invitation={inv} shared={shared} />
        ))}
      </TableBody>
    </Table>
  );
}

interface RowProps {
  invitation: Invitation;
  shared: Omit<InvitationListProps, "invitations">;
}

function InvitationRow({ invitation, shared }: RowProps) {
  const t = useT();
  const row = useInvitationRow(invitation, shared);

  return (
    <TableRow>
      <TableCell className="max-w-xs">
        <div className="truncate font-medium">{row.recipient}</div>
        {row.details && <div className="text-muted-foreground truncate text-xs">{row.details}</div>}
      </TableCell>
      <TableCell className="text-muted-foreground capitalize">{invitation.role}</TableCell>
      <TableCell>
        <Badge variant={row.badge.variant}>{t(row.badge.label)}</Badge>
        {row.expires && <span className="text-muted-foreground ms-2 text-xs">{row.expires}</span>}
      </TableCell>
      <TableCell>
        <DeliveryLabel delivery={invitation.delivery} />
      </TableCell>
      <TableCell className="text-muted-foreground text-sm">{row.created}</TableCell>
      <TableCell>
        <div className="flex justify-end gap-1">
          {row.actions.map((action) => (
            <Button
              key={action.key}
              variant="ghost"
              size="sm"
              onClick={action.run}
              disabled={action.disabled}
              title={action.label}
            >
              <action.icon className="h-4 w-4" />
            </Button>
          ))}
        </div>
      </TableCell>
    </TableRow>
  );
}

/**
 * One invitation as a card. One or two actions show as labeled buttons at the
 * bottom; three move into a menu like the libraries card's.
 */
function InvitationCard({ invitation, shared }: RowProps) {
  const t = useT();
  const row = useInvitationRow(invitation, shared);
  const useMenu = row.actions.length > 2;

  return (
    <DiagnosticsCard>
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1 space-y-0.5">
          <h3 className={cn("font-medium", row.link ? "break-words" : "break-all")}>
            {row.recipient}
          </h3>
          {row.details && (
            <p className="text-muted-foreground text-xs break-words">{row.details}</p>
          )}
        </div>
        {useMenu ? <InvitationCardMenu name={row.recipient} actions={row.actions} /> : null}
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="secondary" className="capitalize">
          {invitation.role}
        </Badge>
        <Badge variant={row.badge.variant}>{t(row.badge.label)}</Badge>
        {row.expires && <span className="text-muted-foreground text-xs">{row.expires}</span>}
      </div>

      <CardFields>
        <CardField label={t("web.admin.settings.invitations.table.delivery")}>
          <DeliveryLabel delivery={invitation.delivery} />
        </CardField>
        <CardField label={t("web.admin.settings.invitations.table.created")}>
          {row.created}
        </CardField>
      </CardFields>

      {!useMenu && row.actions.length > 0 ? (
        // The labels are long, so the buttons stack until the card is wide enough.
        <div className="flex flex-col gap-2 sm:flex-row">
          {row.actions.map((action) => (
            <Button
              key={action.key}
              variant="outline"
              className={cn(
                CARD_ACTION_CLASS,
                action.destructive && "text-destructive hover:text-destructive",
              )}
              onClick={action.run}
              disabled={action.disabled}
            >
              <action.icon aria-hidden="true" />
              {action.label}
            </Button>
          ))}
        </div>
      ) : null}
    </DiagnosticsCard>
  );
}

function InvitationCardMenu({ name, actions }: { name: string; actions: InvitationAction[] }) {
  const t = useT();
  const regular = actions.filter((action) => !action.destructive);
  const destructive = actions.filter((action) => action.destructive);
  const item = (action: InvitationAction) => (
    <DropdownMenuItem
      key={action.key}
      className={MENU_ITEM_CLASS}
      variant={action.destructive ? "destructive" : "default"}
      disabled={action.disabled}
      onSelect={action.run}
    >
      <action.icon aria-hidden="true" />
      {action.label}
    </DropdownMenuItem>
  );
  return (
    // Not modal, like the libraries card menu, so an item can open a dialog.
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="text-muted-foreground data-[state=open]:bg-accent data-[state=open]:text-foreground -me-2 -mt-2 size-11 shrink-0 rounded-[10px]"
          aria-label={t("web.admin.libraries.list.moreActions", { name })}
        >
          <Ellipsis className="size-4" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64 max-w-[calc(100vw-2rem)]">
        {regular.map(item)}
        {regular.length > 0 && destructive.length > 0 ? <DropdownMenuSeparator /> : null}
        {destructive.map(item)}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
