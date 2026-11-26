import { useRef, useState } from "react";
import { Copy } from "lucide-react";
import { toast } from "sonner";
import type { AdminUser } from "@/api/types";
import type { AdminSignInLink } from "@/api/v2/adminUsers";
import { V2ProblemError } from "@/api/v2/request";
import { useCreateSignInLink } from "@/hooks/queries/admin/users";
import { useT } from "@/i18n";
import { copyTextToClipboard } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/datetime";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

/**
 * Makes a single-use sign-in link for a person who lost every device: its
 * page hands the Silo app a code, or lets the person choose a password. The
 * link is always shown to copy; with mail set up and an address on the
 * account it can also be emailed.
 */
export function AdminUserSignInLinkDialog({
  user,
  emailAvailable,
  onClose,
}: {
  user: AdminUser;
  emailAvailable: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const create = useCreateSignInLink();
  const busy = useRef(false);
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<{ link: AdminSignInLink; emailed: boolean } | null>(null);
  const [error, setError] = useState("");
  const canEmail = emailAvailable && user.email !== "";

  async function run(sendEmail: boolean) {
    if (busy.current) return;
    busy.current = true;
    setPending(true);
    setError("");
    try {
      const link = await create.mutateAsync({ id: user.id, sendEmail });
      setResult({ link, emailed: sendEmail });
    } catch (err) {
      setError(
        err instanceof V2ProblemError && err.problemType === "provider_sign_in_required"
          ? t("web.admin.users.signInLinkDialog.providerRequired", { username: user.username })
          : err instanceof Error
            ? err.message
            : t("web.admin.users.signInLinkDialog.failed"),
      );
    } finally {
      busy.current = false;
      setPending(false);
    }
  }

  async function copy(url: string) {
    try {
      await copyTextToClipboard(url);
      toast.success(t("web.admin.users.passwordResetDialog.linkCopied"));
    } catch {
      setError(t("web.admin.users.passwordResetDialog.copyFailed"));
    }
  }

  const link = result?.link;
  const expires = link ? formatDateTime(link.expires_at) : "";

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy.current) onClose();
      }}
    >
      <DialogContent className={cn("min-w-0 sm:max-w-lg", TOUCH_TARGETS)}>
        <DialogHeader>
          <DialogTitle>
            {t("web.admin.users.signInLinkDialog.title", { username: user.username })}
          </DialogTitle>
          <DialogDescription>
            {t("web.admin.users.signInLinkDialog.description", { username: user.username })}
          </DialogDescription>
        </DialogHeader>

        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        {result?.emailed && link?.delivery_status === "sent" && (
          <p role="status" className="text-sm">
            {t("web.admin.users.signInLinkDialog.emailSent", { email: user.email, expires })}
          </p>
        )}
        {result?.emailed && link?.delivery_status !== "sent" && (
          <p role="alert" className="text-sm">
            {t("web.admin.users.signInLinkDialog.emailUnconfirmed")}
          </p>
        )}
        {link && (
          <div className="min-w-0 space-y-2">
            <div className="bg-muted min-w-0 overflow-hidden rounded-md p-2.5">
              {/* On a phone the whole link shows, wrapped; wider screens truncate it. */}
              <code className="block text-xs max-lg:wrap-anywhere lg:truncate">{link.url}</code>
            </div>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.users.signInLinkDialog.linkShare", {
                username: user.username,
                expires,
              })}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="max-sm:w-full"
              onClick={() => void copy(link.url)}
            >
              <Copy className="me-1.5 h-4 w-4" />
              {t("web.admin.users.passwordResetDialog.copyLink")}
            </Button>
          </div>
        )}
        {!link && (
          <p className="text-muted-foreground text-sm">
            {canEmail
              ? t("web.admin.users.signInLinkDialog.emailHint", { email: user.email })
              : user.email === ""
                ? t("web.admin.users.signInLinkDialog.emailHintNoAddress")
                : t("web.admin.users.signInLinkDialog.emailHintNoMail")}
          </p>
        )}

        <DialogFooter>
          {link ? (
            <Button onClick={onClose}>{t("common.done")}</Button>
          ) : (
            <>
              <Button variant="ghost" disabled={pending} onClick={onClose}>
                {t("common.cancel")}
              </Button>
              {canEmail && (
                <Button variant="outline" disabled={pending} onClick={() => void run(true)}>
                  {t("web.admin.users.signInLinkDialog.emailAction")}
                </Button>
              )}
              <Button disabled={pending} onClick={() => void run(false)}>
                {t("web.admin.users.passwordResetDialog.linkAction")}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
