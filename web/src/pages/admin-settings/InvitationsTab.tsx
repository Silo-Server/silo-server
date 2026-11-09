import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useAuth } from "@/hooks/useAuth";
import type { FormEvent } from "react";
import {
  captureInvitationAuthority,
  invitationScope,
  type AdminInvitation as Invitation,
  type InvitationDelivery,
  type InvitationAuthority,
  type InvitationDeliveryChoice,
} from "@/api/v2/invitations";
type InvitationStatus = Invitation["status"];
import {
  useAdminInvitations,
  useInvitationCapabilities,
  useCreateInvitation,
  useResendInvitation,
  useRevokeInvitation,
} from "@/hooks/queries/admin/invitations";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useViewerIsOwner } from "@/hooks/queries/admin/users";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import { effectiveAccessGroupID } from "@/components/UserPolicyFields";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
import { ChoiceOption } from "@/components/ChoiceOption";

import {
  AlertTriangle,
  ArrowRight,
  Copy,
  Link2,
  Mail,
  MailX,
  MailPlus,
  RotateCw,
  Trash2,
} from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";
import { copyTextToClipboard } from "@/lib/clipboard";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import { formatDate } from "@/lib/datetime";
import { useT, type MessageKey, type Translate } from "@/i18n";

/** Example address in the email box. */
const EMAIL_PLACEHOLDER = "them@example.com";
/** Shown in the Delivery column when the delivery is unknown. */
const NO_DELIVERY = "—";

// The claim-link box shown after create/resend. min-w-0 + overflow-hidden on
// every level matters: the URL is one unbreakable token, and without them it
// forces the dialog wider than the viewport on phones.
function ClaimLinkBox({
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
        <code className="block truncate text-xs">{claimUrl}</code>
      </div>
      <p className="text-muted-foreground text-xs">{finePrint}</p>
      <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <Button variant="outline" onClick={() => onCopy(claimUrl)}>
          <Copy className="mr-1.5 h-4 w-4" /> {t("web.admin.settings.invitations.claimLink.copy")}
        </Button>
        <Button onClick={onDone}>{t("common.done")}</Button>
      </div>
    </div>
  );
}

const STATUS_BADGES: Record<
  InvitationStatus,
  { label: MessageKey; variant: "default" | "outline" }
> = {
  pending: { label: "web.admin.settings.invitations.status.pending", variant: "default" },
  accepted: { label: "web.admin.settings.invitations.status.accepted", variant: "outline" },
  expired: { label: "web.admin.settings.invitations.status.expired", variant: "outline" },
  revoked: { label: "web.admin.settings.invitations.status.revoked", variant: "outline" },
};

function deliveryMessage(result: InvitationDelivery, t: Translate) {
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
function isLinkInvitation(invitation: Invitation) {
  return invitation.email === "";
}

function DeliveryLabel({ delivery }: { delivery: Invitation["delivery"] }) {
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

function useMounted() {
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return mounted;
}
export default function InvitationsTab() {
  useAuth();
  return <InvitationManager key={invitationScope()} />;
}
function InvitationManager() {
  const t = useT();
  const viewerId = useAuth().user?.id;
  const viewerIsOwner = useViewerIsOwner(viewerId);
  const capabilities = useInvitationCapabilities();
  const available = capabilities.data?.state === "available";
  const history = useAdminInvitations(available);
  // Invitation links are built on the public URL, and the server refuses to
  // create one without it. Settings that are loading or unreadable block nothing.
  const serverSettings = useAdminServerSettings().data;
  const publicURLMissing =
    serverSettings !== undefined && (serverSettings["server.public_url"] ?? "").trim() === "";
  const invitations = history.data?.pages.flatMap((page) => page.items) ?? [];
  const resend = useResendInvitation();
  const revoke = useRevokeInvitation();
  const mounted = useMounted();
  const busy = useRef(false);
  const createBusy = useRef(false);
  const [createOpen, setCreateOpen] = useState(false);
  const emailDelivery = capabilities.data?.email_delivery === true;
  const [confirmRevoke, setConfirmRevoke] = useState<{
    row: Invitation;
    profileContext: InvitationAuthority;
  } | null>(null);
  const [revokeError, setRevokeError] = useState("");
  const [resendOpen, setResendOpen] = useState(false);
  const [resendResult, setResendResult] = useState<InvitationDelivery | null>(null);
  const [resendError, setResendError] = useState("");
  const [copyError, setCopyError] = useState("");
  async function handleCopy(text: string) {
    try {
      await copyTextToClipboard(text);
      if (mounted.current) {
        setCopyError("");
        toast.success(t("web.admin.settings.invitations.copied"));
      }
    } catch {
      if (mounted.current) setCopyError(t("web.admin.settings.invitations.copyFailed"));
    }
  }
  async function handleResend(id: string, delivery: InvitationDeliveryChoice) {
    if (busy.current) return;
    busy.current = true;
    setResendOpen(true);
    setResendError("");
    setResendResult(null);
    setCopyError("");
    try {
      const result = await resend.mutateAsync({
        id,
        delivery,
        profileContext: captureInvitationAuthority(),
      });
      if (mounted.current) setResendResult(result);
    } catch (error) {
      if (mounted.current)
        setResendError(
          error instanceof Error ? error.message : t("web.admin.settings.invitations.resendFailed"),
        );
    } finally {
      resend.reset();
      busy.current = false;
    }
  }
  async function handleRevoke() {
    if (busy.current || !confirmRevoke) return;
    busy.current = true;
    setRevokeError("");
    try {
      await revoke.mutateAsync({
        id: confirmRevoke.row.id,
        profileContext: confirmRevoke.profileContext,
      });
      if (mounted.current) setConfirmRevoke(null);
    } catch (error) {
      if (mounted.current)
        setRevokeError(
          error instanceof Error ? error.message : t("web.admin.settings.invitations.revokeFailed"),
        );
    } finally {
      revoke.reset();
      busy.current = false;
    }
  }
  if (capabilities.isPending)
    return <p>{t("web.admin.settings.invitations.loadingCapabilities")}</p>;
  if (capabilities.isError)
    return (
      <div role="alert">
        {t("web.admin.settings.invitations.capabilitiesFailed", {
          button: (chunks) => <Button onClick={() => void capabilities.refetch()}>{chunks}</Button>,
        })}
      </div>
    );
  if (!available) return <p>{t("web.admin.settings.invitations.notConfigured")}</p>;
  return (
    <div className="space-y-6">
      <Dialog
        open={confirmRevoke !== null}
        onOpenChange={(open) => {
          if (!open && !busy.current) setConfirmRevoke(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("web.admin.settings.invitations.revoke.title")}</DialogTitle>
            <DialogDescription>
              {confirmRevoke && isLinkInvitation(confirmRevoke.row)
                ? t("web.admin.settings.invitations.revoke.linkDescription")
                : t("web.admin.settings.invitations.revoke.emailDescription", {
                    email: String(confirmRevoke?.row.email),
                  })}
            </DialogDescription>
          </DialogHeader>
          {revokeError && <p role="alert">{revokeError}</p>}
          <Button
            variant="outline"
            disabled={revoke.isPending}
            onClick={() => {
              if (!busy.current) setConfirmRevoke(null);
            }}
          >
            {t("common.cancel")}
          </Button>
          <Button
            variant="destructive"
            disabled={revoke.isPending}
            onClick={() => void handleRevoke()}
          >
            {t("web.admin.settings.invitations.revoke.confirm")}
          </Button>
        </DialogContent>
      </Dialog>
      <Dialog
        open={resendOpen}
        onOpenChange={(open) => {
          if (!open && !busy.current) {
            setResendOpen(false);
            setResendResult(null);
            setCopyError("");
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("web.admin.settings.invitations.resend.title")}</DialogTitle>
            <DialogDescription>
              {resendResult
                ? deliveryMessage(resendResult, t)
                : t("web.admin.settings.invitations.resend.description")}
            </DialogDescription>
          </DialogHeader>
          {resend.isPending && <p>{t("web.admin.settings.invitations.resend.creating")}</p>}
          {resendError && (
            <div role="alert">
              <p>{resendError}</p>
              <p>{t("web.admin.settings.invitations.resend.uncertain")}</p>
              <Button
                onClick={async () => {
                  try {
                    await history.restart();
                  } catch {
                    return;
                  }
                  if (mounted.current) {
                    setResendOpen(false);
                    setResendError("");
                  }
                }}
              >
                {t("web.admin.settings.invitations.reloadHistory")}
              </Button>
            </div>
          )}
          {copyError && <p role="alert">{copyError}</p>}
          {resendResult && (
            <ClaimLinkBox
              claimUrl={resendResult.claim_url}
              finePrint={t("web.admin.settings.invitations.resend.finePrint")}
              onCopy={handleCopy}
              onDone={() => {
                setResendOpen(false);
                setResendResult(null);
                setCopyError("");
              }}
            />
          )}
        </DialogContent>
      </Dialog>
      <div className="flex items-start justify-between gap-4">
        <p className="text-muted-foreground max-w-xl text-sm">
          {t("web.admin.settings.invitations.intro")}
        </p>
        <Dialog
          open={createOpen}
          onOpenChange={(open) => {
            if (!createBusy.current) {
              setCreateOpen(open);
              setCopyError("");
            }
          }}
        >
          <DialogTrigger asChild>
            <Button size="sm" disabled={publicURLMissing}>
              <MailPlus className="mr-1 h-4 w-4" />
              {t("web.admin.settings.invitations.invite.button")}
            </Button>
          </DialogTrigger>
          <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
            <DialogHeader>
              <DialogTitle>{t("web.admin.settings.invitations.invite.title")}</DialogTitle>
              <DialogDescription>
                {t("web.admin.settings.invitations.invite.description")}
              </DialogDescription>
            </DialogHeader>
            {copyError && <p role="alert">{copyError}</p>}
            <CreateInvitationForm
              defaultProfile={capabilities.data?.default_profile === true}
              emailDelivery={emailDelivery}
              onBusy={(value) => {
                createBusy.current = value;
              }}
              onReload={() => history.restart()}
              onClose={() => {
                if (!createBusy.current) {
                  setCreateOpen(false);
                  setCopyError("");
                }
              }}
              onCopy={handleCopy}
            />
          </DialogContent>
        </Dialog>
      </div>
      {publicURLMissing && (
        <div className="flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" aria-hidden="true" />
          <p className="text-[13px] leading-relaxed">
            {t("web.admin.settings.invitations.publicUrlMissing", {
              link: (chunks) => (
                <Link
                  to="/admin/settings/general"
                  className="text-foreground inline-flex items-center gap-1 font-medium hover:underline"
                >
                  {chunks}
                  <ArrowRight className="h-3 w-3" aria-hidden="true" />
                </Link>
              ),
            })}
          </p>
        </div>
      )}
      {resendError && !resendOpen && (
        <div role="alert">
          <p>{resendError}</p>
          <Button
            onClick={async () => {
              try {
                await history.restart();
                if (mounted.current) setResendError("");
              } catch {
                /* The history query displays the failure. */
              }
            }}
          >
            {t("web.admin.settings.invitations.reloadHistory")}
          </Button>
        </div>
      )}
      {history.isError && (
        <div role="alert">
          <p>
            {t("web.admin.settings.invitations.historyFailed", { error: history.error.message })}
          </p>
          <Button onClick={() => void history.restart().catch(() => {})}>
            {t("web.admin.settings.invitations.reloadHistory")}
          </Button>
        </div>
      )}
      {history.isPending ? (
        <p>{t("web.admin.settings.invitations.loading")}</p>
      ) : invitations.length === 0 && !history.isError ? (
        <p>{t("web.admin.settings.invitations.empty")}</p>
      ) : (
        invitations.length > 0 && (
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
                <InvitationRow
                  key={inv.id}
                  invitation={inv}
                  onResend={(delivery) => void handleResend(inv.id, delivery)}
                  emailDelivery={emailDelivery}
                  // Resending re-grants the role, so only the Owner resends an admin invitation.
                  resendAllowed={inv.role !== "admin" || viewerIsOwner}
                  onRevoke={() => {
                    if (busy.current) return;
                    setRevokeError("");
                    setConfirmRevoke({ row: inv, profileContext: captureInvitationAuthority() });
                  }}
                  resending={resend.isPending || revoke.isPending || !!resendError}
                  // A fresh link needs the public URL; revoking one does not.
                  resendBlocked={publicURLMissing}
                />
              ))}
            </TableBody>
          </Table>
        )
      )}
      {history.hasNextPage && (
        <Button
          variant="outline"
          disabled={history.isFetchingNextPage || history.isError}
          onClick={() => void history.fetchNextPage()}
        >
          {t("web.admin.settings.invitations.loadMore")}
        </Button>
      )}
    </div>
  );
}

function InvitationRow({
  invitation,
  onResend,
  emailDelivery,
  onRevoke,
  resending,
  resendBlocked,
  resendAllowed,
}: {
  invitation: Invitation;
  onResend: (delivery: InvitationDeliveryChoice) => void;
  emailDelivery: boolean;
  onRevoke: () => void;
  resending: boolean;
  resendBlocked: boolean;
  resendAllowed: boolean;
}) {
  const t = useT();
  const badge = STATUS_BADGES[invitation.status];
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

  return (
    <TableRow>
      <TableCell className="max-w-xs">
        <div className="truncate font-medium">
          {link ? t("web.admin.settings.invitations.row.linkInvitation") : invitation.email}
        </div>
        {details.length > 0 && (
          <div className="text-muted-foreground truncate text-xs">{details.join(" · ")}</div>
        )}
      </TableCell>
      <TableCell className="text-muted-foreground capitalize">{invitation.role}</TableCell>
      <TableCell>
        <Badge variant={badge.variant}>{t(badge.label)}</Badge>
        {invitation.status === "pending" && (
          <span className="text-muted-foreground ml-2 text-xs">
            {t("web.admin.settings.invitations.row.expires", {
              date: formatDate(invitation.expires_at),
            })}
          </span>
        )}
      </TableCell>
      <TableCell>
        <DeliveryLabel delivery={invitation.delivery} />
      </TableCell>
      <TableCell className="text-muted-foreground text-sm">
        {formatDate(invitation.created_at)}
      </TableCell>
      <TableCell>
        <div className="flex justify-end gap-1">
          {showResend && (
            // Replacing the link never emails; an emailed invitation keeps its address.
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onResend("link")}
              disabled={resending || resendBlocked}
              title={
                link
                  ? t("web.admin.settings.invitations.row.replaceLink")
                  : t("web.admin.settings.invitations.row.copyNewLink")
              }
            >
              {link ? <RotateCw className="h-4 w-4" /> : <Link2 className="h-4 w-4" />}
            </Button>
          )}
          {showResend && !link && emailDelivery && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onResend("email")}
              disabled={resending || resendBlocked}
              title={t("web.admin.settings.invitations.row.emailNewLink")}
            >
              <MailPlus className="h-4 w-4" />
            </Button>
          )}
          {showRevoke && (
            <Button
              variant="ghost"
              size="sm"
              onClick={onRevoke}
              title={t("web.admin.settings.invitations.row.revokeLink")}
              disabled={resending}
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          )}
        </div>
      </TableCell>
    </TableRow>
  );
}

function CreateInvitationForm({
  onClose,
  onCopy,
  defaultProfile,
  emailDelivery,
  onBusy,
  onReload,
}: {
  defaultProfile: boolean;
  emailDelivery: boolean;
  onBusy: (value: boolean) => void;
  onReload: () => Promise<void>;
  onClose: () => void;
  onCopy: (text: string) => Promise<void>;
}) {
  const t = useT();
  const create = useCreateInvitation();
  const { data: accessGroups = [] } = useAccessGroups();
  const { data: libraries = [] } = useAdminLibraries();
  const [delivery, setDelivery] = useState<InvitationDeliveryChoice>(
    emailDelivery ? "email" : "link",
  );
  const deliveryGroup = useId();
  const [email, setEmail] = useState("");
  const [emailInvalid, setEmailInvalid] = useState(false);
  const [role, setRole] = useState<"user" | "admin">("user");
  // Only the server Owner may invite an admin; the server refuses anyone else.
  const viewerId = useAuth().user?.id;
  const viewerIsOwner = useViewerIsOwner(viewerId);
  const [accessGroupID, setAccessGroupID] = useState<number | null>(null);
  const [libraryIDs, setLibraryIDs] = useState<number[] | null>(null);
  const [note, setNote] = useState("");
  const [createProfile, setCreateProfile] = useState(defaultProfile);
  const [showTour, setShowTour] = useState(true);
  // After creation we keep the dialog open to show the claim link — the
  // token is only readable in this response, so this is the one chance to
  // copy it. The delivery outcome describes what the sender confirmed.
  const [result, setResult] = useState<InvitationDelivery | null>(null);
  const [error, setError] = useState("");
  const [needsReload, setNeedsReload] = useState(false);
  const busy = useRef(false);
  const mounted = useMounted();
  const [profileContext] = useState(captureInvitationAuthority);
  const defaultGroup = useMemo(() => accessGroups.find((g) => g.is_default), [accessGroups]);
  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (busy.current || needsReload || (!defaultProfile && createProfile)) return;
    if (delivery === "email" && !isValidEmail(email)) {
      setEmailInvalid(true);
      return;
    }
    busy.current = true;
    onBusy(true);
    setError("");
    const group = effectiveAccessGroupID(role, accessGroupID);
    try {
      const delivered = await create.mutateAsync({
        profileContext,
        body: {
          delivery,
          email: delivery === "email" ? email : undefined,
          role,
          access_group_id: group == null ? undefined : String(group),
          library_ids: libraryIDs == null ? undefined : libraryIDs.map(String),
          create_profile: createProfile,
          show_tour: showTour,
          note: note.trim() || undefined,
        },
      });
      if (mounted.current) setResult(delivered);
    } catch (err) {
      if (mounted.current) {
        setError(
          err instanceof Error ? err.message : t("web.admin.settings.invitations.create.failed"),
        );
        setNeedsReload(true);
      }
    } finally {
      create.reset();
      busy.current = false;
      onBusy(false);
    }
  }

  if (result) {
    return (
      <div className="min-w-0 space-y-4">
        <p className="text-sm">{deliveryMessage(result, t)}</p>
        <ClaimLinkBox
          claimUrl={result.claim_url}
          finePrint={
            result.delivery_status === "not_requested"
              ? t("web.admin.settings.invitations.create.finePrintLink")
              : t("web.admin.settings.invitations.create.finePrintEmail")
          }
          onCopy={onCopy}
          onDone={onClose}
        />
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div role="alert">
          <p>{error}</p>
          <p>{t("web.admin.settings.invitations.create.reloadBeforeRetry")}</p>
          <Button
            type="button"
            onClick={async () => {
              try {
                await onReload();
              } catch {
                if (mounted.current)
                  setError(t("web.admin.settings.invitations.create.reloadFailed"));
                return;
              }
              if (mounted.current) {
                setNeedsReload(false);
                setError("");
              }
            }}
          >
            {t("web.admin.settings.invitations.reloadHistory")}
          </Button>
        </div>
      )}
      {!defaultProfile && (
        <p role="status">{t("web.admin.settings.invitations.create.noDefaultProfile")}</p>
      )}
      <div className="space-y-2">
        <p id={`${deliveryGroup}-label`} className="text-sm leading-none font-medium">
          {t("web.admin.settings.invitations.create.deliveryQuestion")}
        </p>
        <div
          role="radiogroup"
          aria-labelledby={`${deliveryGroup}-label`}
          className="grid gap-2 sm:grid-cols-2"
        >
          <ChoiceOption
            name={deliveryGroup}
            value="link"
            selected={delivery === "link"}
            disabled={create.isPending}
            title={t("web.admin.settings.invitations.create.linkOption")}
            onSelect={setDelivery}
          >
            {t("web.admin.settings.invitations.create.linkOptionBody")}
          </ChoiceOption>
          <ChoiceOption
            name={deliveryGroup}
            value="email"
            selected={delivery === "email"}
            disabled={!emailDelivery || create.isPending}
            title={t("web.admin.settings.invitations.create.emailOption")}
            onSelect={setDelivery}
          >
            {emailDelivery
              ? t("web.admin.settings.invitations.create.emailOptionBody")
              : t("web.admin.settings.invitations.create.emailOptionUnavailable")}
          </ChoiceOption>
        </div>
        {!emailDelivery && (
          <div className="flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" aria-hidden="true" />
            <p className="text-[13px] leading-relaxed">
              {t("web.admin.settings.invitations.create.emailSetup", {
                link: (chunks) => (
                  <Link
                    to="/admin/settings/email"
                    className="text-foreground inline-flex items-center gap-1 font-medium hover:underline"
                  >
                    {chunks}
                    <ArrowRight className="h-3 w-3" aria-hidden="true" />
                  </Link>
                ),
              })}
            </p>
          </div>
        )}
      </div>

      {delivery === "email" && (
        <div className="space-y-2">
          <Label htmlFor="invitation-email">
            {t("web.admin.settings.invitations.create.emailLabel")}
          </Label>
          <Input
            id="invitation-email"
            type="email"
            value={email}
            onChange={(e) => {
              setEmail(e.target.value);
              if (emailInvalid) setEmailInvalid(false);
            }}
            placeholder={EMAIL_PLACEHOLDER}
            aria-invalid={emailInvalid || undefined}
            aria-describedby={emailInvalid ? "invitation-email-error" : undefined}
            autoFocus
            required
          />
          {emailInvalid ? (
            <p id="invitation-email-error" className="text-destructive text-xs">
              {invalidEmailMessage()}
            </p>
          ) : null}
          <p className="text-muted-foreground text-xs">
            {t("web.admin.settings.invitations.create.emailHint")}
          </p>
        </div>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-2">
          <Label>{t("web.admin.settings.invitations.create.accessGroup")}</Label>
          <Select
            value={role === "admin" || accessGroupID === null ? "default" : String(accessGroupID)}
            onValueChange={(v) => setAccessGroupID(v === "default" ? null : Number(v))}
            disabled={role === "admin"}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="default">
                {defaultGroup
                  ? t("web.admin.settings.invitations.create.defaultGroup", {
                      name: defaultGroup.name,
                    })
                  : t("web.admin.settings.invitations.create.serverDefault")}
              </SelectItem>
              {accessGroups
                .filter((g) => !g.is_default)
                .map((g) => (
                  <SelectItem key={g.id} value={String(g.id)}>
                    {g.name}
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label>{t("web.admin.settings.invitations.create.role")}</Label>
          <Select value={role} onValueChange={(value) => setRole(value as "user" | "admin")}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user">
                {t("web.admin.settings.invitations.create.roleUser")}
              </SelectItem>
              <SelectItem value="admin" disabled={!viewerIsOwner}>
                {t("web.admin.settings.invitations.create.roleAdmin")}
              </SelectItem>
            </SelectContent>
          </Select>
          {!viewerIsOwner && (
            <p className="text-muted-foreground text-xs">
              {t("web.admin.settings.invitations.create.ownerOnly")}
            </p>
          )}
        </div>
      </div>

      <LibraryAccessSelector
        libraries={libraries}
        value={libraryIDs}
        onChange={setLibraryIDs}
        allLabel={t("web.admin.settings.invitations.create.inheritGroup")}
        emptyHint={t("web.admin.settings.invitations.create.libraryHint")}
      />

      <div className="space-y-2">
        <Label htmlFor="invitation-note">
          {delivery === "link"
            ? t("web.admin.settings.invitations.create.noteLabel")
            : t("web.admin.settings.invitations.create.personalNoteLabel")}
        </Label>
        <textarea
          id="invitation-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder={t("web.admin.settings.invitations.create.notePlaceholder")}
          rows={2}
          className="border-border bg-background text-foreground focus:border-ring focus:ring-ring/50 w-full resize-y rounded-md border px-3 py-2.5 text-sm shadow-xs transition-[color,box-shadow] outline-none focus:ring-[3px]"
        />
        <p className="text-muted-foreground text-xs">
          {delivery === "link"
            ? t("web.admin.settings.invitations.create.noteHintLink")
            : t("web.admin.settings.invitations.create.noteHintEmail")}
        </p>
      </div>

      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <Label htmlFor="invitation-create-profile">
              {t("web.admin.settings.invitations.create.profileLabel")}
            </Label>
            <p className="text-muted-foreground text-xs">
              {delivery === "link"
                ? t("web.admin.settings.invitations.create.profileHintLink")
                : t("web.admin.settings.invitations.create.profileHintEmail")}
            </p>
          </div>
          <Switch
            id="invitation-create-profile"
            checked={createProfile}
            onCheckedChange={setCreateProfile}
            disabled={!defaultProfile && !createProfile}
          />
        </div>
        <div className="flex items-center justify-between">
          <div>
            <Label htmlFor="invitation-show-tour">
              {t("web.admin.settings.invitations.create.tourLabel")}
            </Label>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.settings.invitations.create.tourHint")}
            </p>
          </div>
          <Switch id="invitation-show-tour" checked={showTour} onCheckedChange={setShowTour} />
        </div>
      </div>

      <div className="flex items-center justify-between pt-2">
        <p className="text-muted-foreground text-xs">
          {t("web.admin.settings.invitations.create.expiry")}
        </p>
        <div className="flex gap-2">
          <Button type="button" variant="ghost" disabled={create.isPending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            type="submit"
            disabled={create.isPending || needsReload || (!defaultProfile && createProfile)}
          >
            {delivery === "link"
              ? create.isPending
                ? t("web.admin.settings.invitations.create.creating")
                : t("web.admin.settings.invitations.create.submitLink")
              : create.isPending
                ? t("web.admin.settings.invitations.create.sending")
                : t("web.admin.settings.invitations.create.submitEmail")}
          </Button>
        </div>
      </div>
    </form>
  );
}
