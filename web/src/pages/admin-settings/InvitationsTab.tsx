import { useRef, useState } from "react";
import { useAuth } from "@/hooks/useAuth";
import {
  captureInvitationAuthority,
  invitationScope,
  type AdminInvitation as Invitation,
  type InvitationDelivery,
  type InvitationAuthority,
  type InvitationDeliveryChoice,
} from "@/api/v2/invitations";
import {
  useAdminInvitations,
  useInvitationCapabilities,
  useResendInvitation,
  useRevokeInvitation,
} from "@/hooks/queries/admin/invitations";
import { useViewerIsOwner } from "@/hooks/queries/admin/users";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";

import { AlertTriangle, MailPlus } from "lucide-react";
import { toast } from "sonner";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";
import { useT } from "@/i18n";

import { CreateInvitationForm } from "./CreateInvitationForm";
import { ClaimLinkBox } from "./InvitationDisplay";
import { InvitationList } from "./InvitationList";
import { deliveryMessage, isLinkInvitation, useMounted } from "./invitationRules";
import {
  FULL_SCREEN_DIALOG_CLASS,
  TOUCH_DIALOG_CLOSE_CLASS,
  TOUCH_FIELD_CLASS,
  TOUCH_PRIMARY_ACTION_CLASS,
} from "./touchClasses";
import { FullScreenDialogHeader, SettingsLink } from "./TouchLayoutParts";

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
          button: (chunks) => (
            <Button className={TOUCH_FIELD_CLASS} onClick={() => void capabilities.refetch()}>
              {chunks}
            </Button>
          ),
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
        <DialogContent className={TOUCH_DIALOG_CLOSE_CLASS}>
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
            className={TOUCH_FIELD_CLASS}
            disabled={revoke.isPending}
            onClick={() => {
              if (!busy.current) setConfirmRevoke(null);
            }}
          >
            {t("common.cancel")}
          </Button>
          <Button
            variant="destructive"
            className={TOUCH_FIELD_CLASS}
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
        <DialogContent className={TOUCH_DIALOG_CLOSE_CLASS}>
          <DialogHeader>
            <DialogTitle>{t("web.admin.settings.invitations.resend.title")}</DialogTitle>
            <DialogDescription className="break-words">
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
                className={TOUCH_FIELD_CLASS}
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
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
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
            <Button size="sm" className={TOUCH_PRIMARY_ACTION_CLASS} disabled={publicURLMissing}>
              <MailPlus className="me-1 h-4 w-4" />
              {t("web.admin.settings.invitations.invite.button")}
            </Button>
          </DialogTrigger>
          {/* Below sm the invite form fills the screen; from sm up it is a centered box. */}
          <DialogContent
            className={cn(
              "max-h-[85vh] overflow-y-auto sm:max-w-lg",
              FULL_SCREEN_DIALOG_CLASS,
              TOUCH_DIALOG_CLOSE_CLASS,
            )}
          >
            <FullScreenDialogHeader
              title={t("web.admin.settings.invitations.invite.title")}
              description={t("web.admin.settings.invitations.invite.description")}
            />
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
              link: (chunks) => <SettingsLink to="/admin/settings/general">{chunks}</SettingsLink>,
            })}
          </p>
        </div>
      )}
      {resendError && !resendOpen && (
        <div role="alert">
          <p>{resendError}</p>
          <Button
            className={TOUCH_FIELD_CLASS}
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
          <p className="break-words">
            {t("web.admin.settings.invitations.historyFailed", { error: history.error.message })}
          </p>
          <Button
            className={TOUCH_FIELD_CLASS}
            onClick={() => void history.restart().catch(() => {})}
          >
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
          <InvitationList
            invitations={invitations}
            emailDelivery={emailDelivery}
            viewerIsOwner={viewerIsOwner}
            resending={resend.isPending || revoke.isPending || !!resendError}
            resendBlocked={publicURLMissing}
            onResend={(inv, delivery) => void handleResend(inv.id, delivery)}
            onRevoke={(inv) => {
              if (busy.current) return;
              setRevokeError("");
              setConfirmRevoke({ row: inv, profileContext: captureInvitationAuthority() });
            }}
          />
        )
      )}
      {history.hasNextPage && (
        <Button
          variant="outline"
          className={TOUCH_PRIMARY_ACTION_CLASS}
          disabled={history.isFetchingNextPage || history.isError}
          onClick={() => void history.fetchNextPage()}
        >
          {t("web.admin.settings.invitations.loadMore")}
        </Button>
      )}
    </div>
  );
}
