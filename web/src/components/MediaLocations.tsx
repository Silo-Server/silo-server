import { useMemo } from "react";
import { Copy, Info } from "lucide-react";
import { toast } from "sonner";
import type { FileVersion } from "@/api/types";
import { Button } from "@/components/ui/button";
import { useT, type Translate } from "@/i18n";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/utils";
import { resolutionScore } from "@/pages/ItemDetail/components/versionRankingUtils";
import { buildQualitySummary } from "@/pages/ItemDetail/components/VersionFlyout";

interface MediaLocationsProps {
  title: string;
  versions: FileVersion[];
  className?: string;
  emptyMessage?: string;
  compact?: boolean;
  summaryBuilder?: (version: FileVersion) => string;
  onShowMediaInfo?: (fileId: number) => void;
}

interface MediaLocationEntry {
  fileId: number;
  versionLabel: string;
  folderName: string;
  folderPath: string;
  fileName: string;
}

function splitMediaPath(
  filePath: string,
  fallbackName: string | undefined,
  t: Translate,
): Omit<MediaLocationEntry, "fileId" | "versionLabel"> | null {
  const lastSlash = Math.max(filePath.lastIndexOf("/"), filePath.lastIndexOf("\\"));
  if (lastSlash < 0) {
    const fileName = fallbackName?.trim() || filePath.trim();
    if (!fileName) return null;

    return {
      folderName: "",
      folderPath: "",
      fileName,
    };
  }

  const folderPath = filePath.slice(0, lastSlash);
  const fileName =
    filePath.slice(lastSlash + 1) || fallbackName?.trim() || t("web.item.locations.unknownFile");
  const folderSegments = folderPath.split(/[\\/]/).filter(Boolean);
  const folderName = folderSegments[folderSegments.length - 1] || folderPath || "/";

  return {
    folderName,
    folderPath,
    fileName,
  };
}

function deriveMediaLocationsWithSummary(
  versions: FileVersion[],
  summaryBuilder: (version: FileVersion) => string,
  t: Translate,
): MediaLocationEntry[] {
  return [...versions]
    .sort((a, b) => {
      const resolutionDelta = resolutionScore(b.resolution) - resolutionScore(a.resolution);
      if (resolutionDelta !== 0) return resolutionDelta;
      if (a.hdr !== b.hdr) return a.hdr ? -1 : 1;
      return a.file_id - b.file_id;
    })
    .map((version, index) => {
      const path = version.file_path?.trim();
      if (!path) return null;

      const location = splitMediaPath(path, version.file_name, t);
      if (!location) return null;

      return {
        ...location,
        fileId: version.file_id,
        versionLabel:
          summaryBuilder(version) || t("web.item.locations.version", { number: index + 1 }),
      };
    })
    .filter((location): location is MediaLocationEntry => Boolean(location));
}

async function copyText(text: string, successMessage: string, failureMessage: string) {
  try {
    await copyTextToClipboard(text);
    toast.success(successMessage);
  } catch {
    toast.error(failureMessage);
  }
}

export default function MediaLocations({
  title,
  versions,
  className,
  emptyMessage,
  compact = false,
  summaryBuilder = buildQualitySummary,
  onShowMediaInfo,
}: MediaLocationsProps) {
  const t = useT();
  const locations = useMemo(
    () => deriveMediaLocationsWithSummary(versions, summaryBuilder, t),
    [versions, summaryBuilder, t],
  );

  if (locations.length === 0) {
    if (!emptyMessage) return null;

    return (
      <section className={cn("space-y-3", className)}>
        <h2 className="text-base font-semibold tracking-tight">{title}</h2>
        <div className="text-muted-foreground bg-muted/30 rounded-lg border px-3 py-2 text-sm">
          {emptyMessage}
        </div>
      </section>
    );
  }

  return (
    <section className={cn("space-y-3", className)}>
      <h2 className="text-base font-semibold tracking-tight">{title}</h2>

      <div className="space-y-2">
        {locations.map((location) => (
          <div
            key={location.fileId}
            className={cn(
              "bg-background/70 rounded-lg border px-3 py-2.5",
              compact && "px-2.5 py-2",
            )}
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 space-y-1.5">
                <div className="text-sm font-medium">{location.versionLabel}</div>
                <div className="min-w-0 font-mono text-xs leading-relaxed sm:text-[0.75rem]">
                  {/* A path reads left to right in every language; inline, so it
                    keeps the panel's alignment. */}
                  <span dir="ltr">
                    {location.folderName ? (
                      <span
                        className="text-muted-foreground"
                        title={location.folderPath}
                        aria-label={t("web.item.locations.parentFolder", {
                          path: location.folderPath,
                        })}
                      >
                        {t("web.item.locations.folderName", { folder: location.folderName })}
                      </span>
                    ) : null}
                    <span className="text-foreground ms-1 break-all">{location.fileName}</span>
                  </span>
                </div>
              </div>

              <div className="flex shrink-0 items-center gap-1">
                {onShowMediaInfo ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-foreground h-7 w-7"
                    onClick={() => onShowMediaInfo(location.fileId)}
                    title={t("web.item.locations.viewMediaInfo")}
                    aria-label={t("web.item.locations.viewMediaInfoFor", {
                      file: location.fileName,
                    })}
                  >
                    <Info className="h-3.5 w-3.5" />
                  </Button>
                ) : null}
                {location.folderPath ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-foreground h-7 w-7"
                    onClick={() =>
                      copyText(
                        location.folderPath,
                        t("web.item.dialog.copiedFolderPath"),
                        t("web.item.dialog.copyPathFailed"),
                      )
                    }
                    title={t("web.item.locations.copyFolderPath")}
                    aria-label={t("web.item.locations.copyFolderPathFor", {
                      file: location.fileName,
                    })}
                  >
                    <Copy className="h-3.5 w-3.5" />
                  </Button>
                ) : null}
              </div>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}
