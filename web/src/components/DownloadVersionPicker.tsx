import { useState } from "react";
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
import { formatFileSize } from "@/pages/ItemDetail/components/versionFormatUtils";
import { buildDirectDownloadUrl } from "@/hooks/queries/downloads";
import { buildQualitySummary, sortByResolution } from "@/pages/ItemDetail/components/VersionFlyout";

interface DownloadVersionPickerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  versions: FileVersion[];
  title?: string;
}

export default function DownloadVersionPicker({
  open,
  onOpenChange,
  versions,
  title,
}: DownloadVersionPickerProps) {
  const sorted = sortByResolution(versions);
  const [downloading, setDownloading] = useState<number | null>(null);

  const handleDownload = async (version: FileVersion) => {
    const url = buildDirectDownloadUrl(version.file_id);
    setDownloading(version.file_id);
    try {
      const res = await fetch(url, { method: "HEAD" });
      if (!res.ok) {
        if (res.status === 403) toast.error("You are not allowed to download this file");
        else if (res.status === 429) toast.error("Download limit reached. Try again later");
        else toast.error("Download failed. Try again later");
        return;
      }
      const a = document.createElement("a");
      a.href = url;
      a.download = "";
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      onOpenChange(false);
    } catch {
      toast.error("Network error. Check your connection and try again");
    } finally {
      setDownloading(null);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Download{title ? `: ${title}` : ""}</DialogTitle>
          <DialogDescription>
            Choose a version to download. Make sure you have enough disk space.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          {sorted.map((version) => {
            const quality = buildQualitySummary(version);
            const size = formatFileSize(version.file_size);

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
