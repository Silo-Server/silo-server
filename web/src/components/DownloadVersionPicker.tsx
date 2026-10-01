import { useLayoutEffect, useRef, useState } from "react";
import { Download, Loader2 } from "lucide-react";
import { toast } from "sonner";
import type { FileVersion } from "@/api/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { formatFileSize } from "@/lib/mediaFormat";
import { StaleApiRequestContextError } from "@/api/client";
import { launchDirectDownload } from "@/api/v2/directDownloads";
import { buildQualitySummary, sortByResolution } from "@/pages/ItemDetail/components/VersionFlyout";

interface DownloadVersionPickerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  versions: FileVersion[];
  title?: string;
  summaryBuilder?: (version: FileVersion) => string;
}

export default function DownloadVersionPicker({
  open,
  onOpenChange,
  versions,
  title,
  summaryBuilder,
}: DownloadVersionPickerProps) {
  const sorted = sortByResolution(versions);
  const [downloading, setDownloading] = useState<number | null>(null);

  const active = useRef<symbol | null>(null);
  useLayoutEffect(() => {
    active.current = null;
    setDownloading(null);
    return () => {
      active.current = null;
    };
  }, [open, versions]);

  const handleDownload = async (version: FileVersion) => {
    if (active.current || !open) return;
    const attempt = Symbol();
    active.current = attempt;
    const isCurrent = () => active.current === attempt;
    setDownloading(version.file_id);
    try {
      await launchDirectDownload(version.file_id, isCurrent);
      if (isCurrent()) onOpenChange(false);
    } catch (error) {
      if (isCurrent() && !(error instanceof StaleApiRequestContextError))
        toast.error("Download could not be started. Check access and try again.");
    } finally {
      if (isCurrent()) {
        active.current = null;
        setDownloading(null);
      }
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Download{title ? `: ${title}` : ""}</DialogTitle>
          <DialogDescription>
            Choose a file to download. Make sure you have enough disk space.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          {sorted.map((version) => {
            const quality = summaryBuilder?.(version) || buildQualitySummary(version);
            const size = summaryBuilder ? "" : formatFileSize(version.file_size);

            return (
              <button
                key={version.file_id}
                type="button"
                onClick={() => handleDownload(version)}
                disabled={downloading !== null}
                className="border-border/50 bg-accent/30 hover:bg-accent/60 flex w-full items-center gap-3 rounded-xl border px-4 py-3 text-left transition-colors disabled:opacity-50"
              >
                <span className="bg-primary/10 text-primary flex size-9 shrink-0 items-center justify-center rounded-full">
                  {downloading === version.file_id ? (
                    <Loader2 className="size-4 animate-spin" />
                  ) : (
                    <Download className="size-4" />
                  )}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="text-foreground block text-sm font-medium">{quality}</span>
                  {size && <span className="text-muted-foreground block text-xs">{size}</span>}
                </span>
              </button>
            );
          })}
        </div>

        {sorted.length > 1 && (
          <p className="text-muted-foreground text-xs">Larger files require more storage space.</p>
        )}
      </DialogContent>
    </Dialog>
  );
}
