import { Loader2, RefreshCw, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { RefreshItemMetadataMode } from "@/hooks/queries/items";
import { useT } from "@/i18n";

interface RefreshMetadataDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (mode: RefreshItemMetadataMode) => void;
  isPending?: boolean;
}

export default function RefreshMetadataDialog({
  open,
  onOpenChange,
  onConfirm,
  isPending = false,
}: RefreshMetadataDialogProps) {
  const t = useT();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("web.item.dialog.refreshMetadataTitle")}</DialogTitle>
          <DialogDescription>{t("web.item.refresh.description")}</DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <button
            type="button"
            disabled={isPending}
            onClick={() => onConfirm("quick")}
            className="border-border bg-surface hover:bg-surface/80 flex w-full items-start gap-3 rounded-xl border p-4 text-start transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          >
            {isPending ? (
              <Loader2 className="text-muted-foreground mt-0.5 size-5 animate-spin" />
            ) : (
              <RefreshCw className="text-muted-foreground mt-0.5 size-5" />
            )}
            <div className="space-y-1">
              <div className="text-sm font-semibold">{t("web.item.refresh.quickTitle")}</div>
              <div className="text-muted-foreground text-sm">{t("web.item.refresh.quickBody")}</div>
            </div>
          </button>

          <button
            type="button"
            disabled={isPending}
            onClick={() => onConfirm("complete")}
            className="border-border bg-surface hover:bg-surface/80 flex w-full items-start gap-3 rounded-xl border p-4 text-start transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          >
            {isPending ? (
              <Loader2 className="text-muted-foreground mt-0.5 size-5 animate-spin" />
            ) : (
              <RotateCcw className="text-muted-foreground mt-0.5 size-5" />
            )}
            <div className="space-y-1">
              <div className="text-sm font-semibold">{t("web.item.refresh.completeTitle")}</div>
              <div className="text-muted-foreground text-sm">
                {t("web.item.refresh.completeBody")}
              </div>
            </div>
          </button>
        </div>

        <div className="flex justify-end">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={isPending}>
            {t("common.cancel")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
