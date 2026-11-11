import { useState } from "react";
import type { MediaRequest, RequestOption } from "@/api/types";
import { RequestOptionFields } from "@/components/RequestOptionFields";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { requestOptionsToSend, type RequestOptionPicks } from "@/lib/mediaRequests";
import { useT } from "@/i18n";

/** A request an admin is approving, and the settings they may choose for it. */
export interface ApprovePrompt {
  request: MediaRequest;
  options: readonly RequestOption[];
}

interface ApproveOptionsDialogProps {
  prompt: ApprovePrompt | null;
  /** Approves with the picks that differ from what would apply anyway. */
  onConfirm: (request: MediaRequest, options?: Record<string, string>) => void;
  onOpenChange: (open: boolean) => void;
}

/**
 * Asks the approving admin for the download server settings of a request (a
 * quality profile, a folder). Each select starts on the requester's pick, or
 * on the server's own setting.
 */
export function ApproveOptionsDialog({
  prompt,
  onConfirm,
  onOpenChange,
}: ApproveOptionsDialogProps) {
  const t = useT();
  const [picks, setPicks] = useState<RequestOptionPicks>({});
  const close = (next: boolean) => {
    if (!next) setPicks({});
    onOpenChange(next);
  };
  return (
    <Dialog open={prompt !== null} onOpenChange={close}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("web.admin.requests.action.approve")}</DialogTitle>
          <DialogDescription>{prompt?.request.title}</DialogDescription>
        </DialogHeader>
        <RequestOptionFields options={prompt?.options ?? []} picks={picks} onChange={setPicks} />
        <DialogFooter>
          <Button variant="outline" onClick={() => close(false)}>
            {t("common.cancel")}
          </Button>
          <Button
            onClick={() => {
              if (!prompt) return;
              onConfirm(prompt.request, requestOptionsToSend(prompt.options, picks));
              close(false);
            }}
          >
            {t("web.admin.requests.action.approve")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
