import { useState } from "react";
import { Loader2 } from "lucide-react";
import type { RequestMediaResult, RequestOption } from "@/api/types";
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
import { useCreateMediaRequest } from "@/hooks/queries/useRequests";
import {
  requestInputFromMediaResult,
  requestOptionsToSend,
  type RequestOptionPicks,
} from "@/lib/mediaRequests";
import { useT } from "@/i18n";

interface RequestOptionsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  item: RequestMediaResult;
  /** What the viewer may choose; the caller opens the dialog only when there is something. */
  options: readonly RequestOption[];
}

/**
 * Asks for the download server settings a title's request may choose (a
 * quality profile, a folder) before sending it. A series picks them in the
 * seasons dialog instead.
 */
export function RequestOptionsDialog({
  open,
  onOpenChange,
  item,
  options,
}: RequestOptionsDialogProps) {
  const t = useT();
  const createRequest = useCreateMediaRequest();
  const [picks, setPicks] = useState<RequestOptionPicks>({});

  const close = (next: boolean) => {
    if (!next) setPicks({});
    onOpenChange(next);
  };
  const submit = () => {
    const chosen = requestOptionsToSend(options, picks);
    createRequest.mutate(
      { ...requestInputFromMediaResult(item), ...(chosen && { options: chosen }) },
      { onSuccess: () => close(false) },
    );
  };

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {item.media_type === "series"
              ? t("web.detail.request.requestSeries")
              : t("web.detail.request.requestMovie")}
          </DialogTitle>
          <DialogDescription>{item.title}</DialogDescription>
        </DialogHeader>
        <RequestOptionFields
          options={options}
          picks={picks}
          onChange={setPicks}
          disabled={createRequest.isPending}
        />
        <DialogFooter>
          <Button variant="outline" onClick={() => close(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} disabled={createRequest.isPending} className="gap-2">
            {createRequest.isPending && <Loader2 className="size-4 animate-spin" />}
            {t("web.requests.request")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
