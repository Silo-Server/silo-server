import { Loader2, RefreshCw } from "lucide-react";

import type { FileVersion } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  type AdminTrickplayFile,
  useAdminItemTrickplay,
  useRegenerateItemTrickplay,
} from "@/hooks/queries/admin/trickplay";
import { useT, type Translate } from "@/i18n";
import { formatDateTime } from "@/lib/datetime";
import { buildQualitySummary } from "@/pages/ItemDetail/components/VersionFlyout";

import { trickplayFileSummary, trickplayStateLabel } from "./trickplayStatus";

interface TrickplayStatusDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  itemId: string;
  /** The item's versions, to name its files; unnamed files show their id. */
  versions?: FileVersion[];
}

function fileLabel(
  file: AdminTrickplayFile,
  versions: FileVersion[] | undefined,
  t: Translate,
): string {
  const fallback = t("web.admin.common.trickplay.file.fallback", { id: file.file_id });
  const version = versions?.find((v) => String(v.file_id) === file.file_id);
  if (!version) return fallback;
  return version.file_name || buildQualitySummary(version) || fallback;
}

function FileRow({ file, label }: { file: AdminTrickplayFile; label: string }) {
  const t = useT();
  const summary = trickplayFileSummary(file);
  // Regeneration keeps serving the previous previews until the new ones publish.
  const stillServing = file.servable && (file.state === "pending" || file.state === "running");
  return (
    <li className="space-y-1 rounded-md border px-3 py-2 text-sm">
      <div className="flex items-baseline justify-between gap-3">
        <span className="min-w-0 truncate font-medium" title={label}>
          {label}
        </span>
        <span className={file.state === "unusable" ? "text-destructive" : "text-muted-foreground"}>
          {trickplayStateLabel(file)}
        </span>
      </div>
      {summary || file.generated_at ? (
        <div className="text-muted-foreground text-xs">
          {[
            summary,
            file.generated_at
              ? t("web.admin.common.trickplay.file.made", {
                  date: formatDateTime(file.generated_at),
                })
              : "",
          ]
            .filter(Boolean)
            .join(" · ")}
        </div>
      ) : null}
      {stillServing ? (
        <div className="text-muted-foreground text-xs">
          {t("web.admin.common.trickplay.file.stillServing")}
        </div>
      ) : null}
      {file.last_error ? <div className="text-destructive text-xs">{file.last_error}</div> : null}
    </li>
  );
}

/**
 * An item's seek previews, file by file, with the admin action that makes them
 * again. Read on open, not with the page: most visits never look.
 */
export function TrickplayStatusDialog({
  open,
  onOpenChange,
  itemId,
  versions,
}: TrickplayStatusDialogProps) {
  const t = useT();
  const status = useAdminItemTrickplay(itemId, { enabled: open });
  const regenerate = useRegenerateItemTrickplay();
  const files = status.data ?? [];
  const loadingLabel = t("web.admin.common.trickplay.dialog.loading");
  const allOff = files.length > 0 && files.every((file) => file.state === "off");

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("web.admin.common.trickplay.dialog.title")}</DialogTitle>
          <DialogDescription>
            {t("web.admin.common.trickplay.dialog.description")}
          </DialogDescription>
        </DialogHeader>
        {status.isLoading ? (
          <div className="text-muted-foreground flex items-center gap-2 text-sm">
            <Loader2 className="size-4 animate-spin" /> {loadingLabel}
          </div>
        ) : status.isError ? (
          <p className="text-destructive text-sm">
            {status.error instanceof Error
              ? status.error.message
              : t("web.admin.common.trickplay.dialog.readFailed")}
          </p>
        ) : (
          <ul className="max-h-80 space-y-2 overflow-y-auto">
            {files.map((file) => (
              <FileRow key={file.file_id} file={file} label={fileLabel(file, versions, t)} />
            ))}
          </ul>
        )}
        {allOff ? (
          <p className="text-muted-foreground text-xs">
            {t("web.admin.common.trickplay.dialog.allOff")}
          </p>
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("web.admin.common.trickplay.dialog.close")}
          </Button>
          <Button
            disabled={!status.isSuccess || allOff || regenerate.isPending}
            onClick={() => regenerate.mutate(itemId)}
          >
            <RefreshCw className={`size-4 ${regenerate.isPending ? "animate-spin" : ""}`} />
            {t("web.admin.common.trickplay.dialog.makeAgain")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
