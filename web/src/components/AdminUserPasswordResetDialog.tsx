import { useRef, useState } from "react";
import { Copy } from "lucide-react";
import { toast } from "sonner";
import type { AdminUser } from "@/api/types";
import type { AdminPasswordReset } from "@/api/v2/adminUsers";
import { useIssuePasswordReset } from "@/hooks/queries/admin/users";
import { copyTextToClipboard } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/datetime";
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
 * Lets an admin help a locked-out account without handling its password:
 * email the account a single-use reset link, or create one to share.
 */
export function AdminUserPasswordResetDialog({
  user,
  emailAvailable,
  linkAvailable,
  onClose,
}: {
  user: AdminUser;
  emailAvailable: boolean;
  linkAvailable: boolean;
  onClose: () => void;
}) {
  const issue = useIssuePasswordReset();
  const busy = useRef(false);
  const [result, setResult] = useState<AdminPasswordReset | null>(null);
  const [error, setError] = useState("");
  const canEmail = emailAvailable && user.email !== "";

  function send(delivery: "email" | "link") {
    if (busy.current) return;
    busy.current = true;
    setError("");
    issue
      .mutateAsync({ id: user.id, delivery })
      .then(setResult)
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : "Could not create a reset link.");
      })
      .finally(() => {
        busy.current = false;
      });
  }

  async function copy(url: string) {
    try {
      await copyTextToClipboard(url);
      toast.success("Link copied");
    } catch {
      setError("Could not copy the link. Select it and copy it manually.");
    }
  }

  const expires = result ? formatDateTime(result.expires_at) : "";
  // An unconfirmed email leaves the admin to retry or switch to a link.
  const unconfirmed = result?.delivery === "email" && result.delivery_status !== "sent";
  const done = result !== null && !unconfirmed;
  const resetURL = result?.reset_url;

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy.current) onClose();
      }}
    >
      <DialogContent className="min-w-0 sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Reset password</DialogTitle>
          <DialogDescription>
            {user.username} gets a link to choose a new password. You never see it. The link works
            once, expires in 24 hours, and signs them out on every device. A new link cancels any
            earlier one.
          </DialogDescription>
        </DialogHeader>

        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}

        {result?.delivery === "email" && result.delivery_status === "sent" && (
          <p role="status" className="text-sm">
            Sent a reset link to {user.email}. It expires {expires}.
          </p>
        )}
        {unconfirmed && (
          <p role="alert" className="text-sm">
            The mail server did not confirm delivery. Send it again, or create a link to share.
          </p>
        )}
        {resetURL && (
          <div className="min-w-0 space-y-2">
            <div className="bg-muted min-w-0 overflow-hidden rounded-md p-2.5">
              <code className="block truncate text-xs">{resetURL}</code>
            </div>
            <p className="text-muted-foreground text-xs">
              Share this link only with {user.username}. Anyone who has it can set the account's
              password until it expires {expires}.
            </p>
            <Button variant="outline" size="sm" onClick={() => void copy(resetURL)}>
              <Copy className="mr-1.5 h-4 w-4" /> Copy link
            </Button>
          </div>
        )}

        {!done && (!canEmail || !linkAvailable) && (
          <p className="text-muted-foreground text-xs">
            {!linkAvailable
              ? "Set the server's public URL in Settings to create reset links."
              : user.email === ""
                ? "This account has no email address, so share a link instead."
                : "Set up email in Settings to send reset links."}
          </p>
        )}

        <DialogFooter>
          {done ? (
            <Button onClick={onClose}>Done</Button>
          ) : (
            <>
              <Button
                variant="outline"
                disabled={!linkAvailable || issue.isPending}
                onClick={() => send("link")}
              >
                Create link to share
              </Button>
              <Button disabled={!canEmail || issue.isPending} onClick={() => send("email")}>
                Email reset link
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
