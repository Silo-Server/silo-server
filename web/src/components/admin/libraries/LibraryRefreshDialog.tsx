import { DatabaseBackup, Loader2, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { LibraryRefreshMode } from "@/hooks/queries/admin/libraries";
import { useT } from "@/i18n";

interface LibraryRefreshDialogProps {
  libraryName: string | null;
  onOpenChange: (open: boolean) => void;
  onConfirm: (mode: LibraryRefreshMode) => void;
  isPending?: boolean;
}

export function LibraryRefreshDialog({
  libraryName,
  onOpenChange,
  onConfirm,
  isPending = false,
}: LibraryRefreshDialogProps) {
  const t = useT();
  return (
    <Dialog open={libraryName !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("web.admin.common.libraryEditor.refresh.title")}</DialogTitle>
          <DialogDescription>
            {libraryName === null
              ? t("web.admin.common.libraryEditor.refresh.descriptionUnnamed")
              : t("web.admin.common.libraryEditor.refresh.description", { name: libraryName })}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <button
            type="button"
            disabled={isPending}
            onClick={() => onConfirm("quick")}
            className="border-border bg-surface hover:bg-surface/80 flex w-full items-start gap-3 rounded-xl border p-4 text-start transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          >
            {isPending ? (
              <Loader2 className="text-muted-foreground mt-0.5 size-5 shrink-0 animate-spin" />
            ) : (
              <RefreshCw className="text-muted-foreground mt-0.5 size-5 shrink-0" />
            )}
            <div className="space-y-1">
              <div className="text-sm font-semibold">
                {t("web.admin.common.libraryEditor.refresh.missingTitle")}
              </div>
              <div className="text-muted-foreground text-sm">
                {t("web.admin.common.libraryEditor.refresh.missingDescription")}
              </div>
            </div>
          </button>

          <button
            type="button"
            disabled={isPending}
            onClick={() => onConfirm("full")}
            className="border-border bg-surface hover:bg-surface/80 flex w-full items-start gap-3 rounded-xl border p-4 text-start transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          >
            {isPending ? (
              <Loader2 className="text-muted-foreground mt-0.5 size-5 shrink-0 animate-spin" />
            ) : (
              <DatabaseBackup className="text-muted-foreground mt-0.5 size-5 shrink-0" />
            )}
            <div className="space-y-1">
              <div className="text-sm font-semibold">
                {t("web.admin.common.libraryEditor.refresh.allTitle")}
              </div>
              <div className="text-muted-foreground text-sm">
                {t("web.admin.common.libraryEditor.refresh.allDescription")}
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
