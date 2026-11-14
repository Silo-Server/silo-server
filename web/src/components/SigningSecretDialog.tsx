import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useT } from "@/i18n";
import { copyTextToClipboard } from "@/lib/clipboard";

/**
 * One-time reveal of a webhook signing secret (profile webhooks and admin
 * server channels). Open while `secret` is non-null.
 */
export function SigningSecretDialog({
  secret,
  onClose,
}: {
  secret: string | null;
  onClose: () => void;
}) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <Dialog open={secret != null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("web.common.signingSecret.title")}</DialogTitle>
          <DialogDescription>{t("web.common.signingSecret.description")}</DialogDescription>
        </DialogHeader>
        <div className="bg-muted flex items-center gap-2 rounded-lg p-3 font-mono text-xs break-all">
          <span className="min-w-0 flex-1">{secret}</span>
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 shrink-0"
            aria-label={
              copied ? t("web.common.signingSecret.copied") : t("web.common.signingSecret.copy")
            }
            onClick={async () => {
              if (!secret) return;
              try {
                await copyTextToClipboard(secret);
                setCopied(true);
              } catch {
                toast.error(t("web.common.signingSecret.copyFailed"));
              }
            }}
          >
            {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
          </Button>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{t("web.common.signingSecret.saved")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
