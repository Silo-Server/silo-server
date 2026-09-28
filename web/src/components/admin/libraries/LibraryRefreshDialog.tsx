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
  return (
    <Dialog open={libraryName !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Refresh Library Metadata</DialogTitle>
          <DialogDescription>
            Choose how much of {libraryName ?? "this library"} to refresh from its metadata
            providers.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <button
            type="button"
            disabled={isPending}
            onClick={() => onConfirm("quick")}
            className="border-border bg-surface hover:bg-surface/80 flex w-full items-start gap-3 rounded-xl border p-4 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          >
            {isPending ? (
              <Loader2 className="text-muted-foreground mt-0.5 size-5 shrink-0 animate-spin" />
            ) : (
              <RefreshCw className="text-muted-foreground mt-0.5 size-5 shrink-0" />
            )}
            <div className="space-y-1">
              <div className="text-sm font-semibold">Refresh Missing Metadata</div>
              <div className="text-muted-foreground text-sm">
                Refresh matched items that were never refreshed, are missing an overview or artwork,
                or failed a previous refresh.
              </div>
            </div>
          </button>

          <button
            type="button"
            disabled={isPending}
            onClick={() => onConfirm("full")}
            className="border-border bg-surface hover:bg-surface/80 flex w-full items-start gap-3 rounded-xl border p-4 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          >
            {isPending ? (
              <Loader2 className="text-muted-foreground mt-0.5 size-5 shrink-0 animate-spin" />
            ) : (
              <DatabaseBackup className="text-muted-foreground mt-0.5 size-5 shrink-0" />
            )}
            <div className="space-y-1">
              <div className="text-sm font-semibold">Refresh All Metadata</div>
              <div className="text-muted-foreground text-sm">
                Re-fetch every item from its providers, including titles, artwork, seasons, and
                episodes. Large libraries can take hours. Locked fields and artwork stay as they
                are.
              </div>
            </div>
          </button>
        </div>

        <div className="flex justify-end">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={isPending}>
            Cancel
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
