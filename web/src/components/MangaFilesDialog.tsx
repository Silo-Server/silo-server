import { Folder, Loader2 } from "lucide-react";
import type { MangaChapterFile } from "@/api/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useMangaSeriesFiles } from "@/hooks/queries/catalogRead";
import { useT, type Translate } from "@/i18n";
import { prettifyVolumeLabel } from "@/lib/mangaChapters";
import { formatFileSize } from "@/lib/mediaFormat";

// fileRowLabel describes the chapter a file backs: its volume token when
// present, otherwise a chapter form mirroring the series list labels.
function fileRowLabel(file: MangaChapterFile, t: Translate): string {
  if (file.volume?.trim()) {
    return prettifyVolumeLabel(file.volume);
  }
  if (typeof file.chapter_index === "number") {
    return t("web.item.mangaFiles.chapterNumber", { number: file.chapter_index });
  }
  return file.title?.trim() || t("web.item.mangaFiles.chapter");
}

// MangaFilesDialog shows the local files backing a manga series: the folder(s)
// the chapters live in and one row per file. Paths are server-stripped for
// viewers without file-path visibility, so those users see names and sizes.
export default function MangaFilesDialog({
  contentId,
  title,
  open,
  onOpenChange,
}: {
  contentId: string;
  title?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const t = useT();
  const { data, isLoading, error } = useMangaSeriesFiles(contentId, open);
  const files = data?.files ?? [];
  const totalBytes = files.reduce((sum, file) => sum + (file.file_size || 0), 0);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] gap-4 sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="truncate pe-6">
            {title
              ? t("web.item.mangaFiles.titleWithName", { title })
              : t("web.item.mangaFiles.title")}
          </DialogTitle>
          <DialogDescription>
            {files.length > 0
              ? t("web.item.mangaFiles.summary", {
                  count: files.length,
                  file_count: files.length,
                  size: formatFileSize(totalBytes),
                })
              : t("web.item.mangaFiles.description")}
          </DialogDescription>
        </DialogHeader>

        {isLoading ? (
          <div className="flex items-center justify-center py-10">
            <Loader2 className="text-muted-foreground size-6 animate-spin" />
          </div>
        ) : error ? (
          <p className="text-destructive py-6 text-sm">{t("web.item.mangaFiles.loadFailed")}</p>
        ) : (
          <div className="min-h-0 space-y-4 overflow-y-auto">
            {(data?.folder_paths?.length ?? 0) > 0 && (
              <div className="space-y-1.5">
                {data?.folder_paths?.map((path) => (
                  <div
                    key={path}
                    className="text-muted-foreground flex items-start gap-2 font-mono text-xs break-all"
                  >
                    <Folder className="mt-0.5 size-3.5 flex-shrink-0" />
                    {path}
                  </div>
                ))}
              </div>
            )}
            {files.length === 0 ? (
              <p className="text-muted-foreground py-4 text-sm">{t("web.item.mangaFiles.empty")}</p>
            ) : (
              <ul className="divide-border/40 border-border/40 divide-y rounded-md border">
                {files.map((file) => (
                  <li
                    key={`${file.content_id}-${file.file_name}`}
                    className="flex items-baseline gap-3 px-3 py-2"
                  >
                    <span className="text-foreground/90 w-28 flex-shrink-0 truncate text-xs font-medium">
                      {fileRowLabel(file, t)}
                    </span>
                    <span
                      className="text-muted-foreground min-w-0 flex-1 truncate font-mono text-xs"
                      title={file.file_path || file.file_name}
                    >
                      {file.file_name}
                    </span>
                    <span className="text-muted-foreground flex-shrink-0 text-xs tabular-nums">
                      {file.file_size > 0 ? formatFileSize(file.file_size) : ""}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
