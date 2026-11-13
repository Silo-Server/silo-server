import type { ChangeEvent } from "react";
import { ImageIcon, Trash2 } from "lucide-react";

import type { Library } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { useT } from "@/i18n";
import { useDeleteLibraryPoster, useUploadLibraryPoster } from "@/hooks/queries/admin/libraries";

// Shown on the upload button while a poster uploads.
const UPLOADING = "...";

export function LibraryPosterSection({ library }: { library: Library }) {
  const t = useT();
  const uploadMutation = useUploadLibraryPoster();
  const deleteMutation = useDeleteLibraryPoster();
  const fileInputId = `poster-upload-${library.id}`;

  function handleFileChange(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    uploadMutation.mutate({ id: library.id, file });
    e.target.value = "";
  }

  return (
    <div className="space-y-1.5">
      <Label>{t("web.admin.common.libraryEditor.poster.label")}</Label>
      <div className="flex flex-wrap items-center gap-2">
        {library.poster_url ? (
          <img
            src={library.poster_url}
            alt={t("web.admin.common.libraryEditor.poster.alt", { name: library.name })}
            className="border-border h-14 flex-shrink-0 rounded border object-cover"
            style={{ aspectRatio: "16/9" }}
          />
        ) : (
          <div
            className="border-border bg-muted/30 flex h-14 flex-shrink-0 items-center justify-center rounded border border-dashed"
            style={{ aspectRatio: "16/9" }}
          >
            <ImageIcon className="text-muted-foreground/40 h-4 w-4" />
          </div>
        )}
        <input
          id={fileInputId}
          type="file"
          accept="image/jpeg,image/png,image/webp"
          className="hidden"
          onChange={handleFileChange}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-11 text-xs sm:h-8"
          onClick={() => document.getElementById(fileInputId)?.click()}
          disabled={uploadMutation.isPending}
        >
          {uploadMutation.isPending
            ? UPLOADING
            : library.poster_url
              ? t("web.admin.common.libraryEditor.poster.replace")
              : t("web.admin.common.libraryEditor.poster.upload")}
        </Button>
        {library.poster_url && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="text-muted-foreground hover:text-destructive size-11 sm:size-8"
            onClick={() => deleteMutation.mutate(library.id)}
            disabled={deleteMutation.isPending}
            title={t("web.admin.common.libraryEditor.poster.remove")}
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        )}
      </div>
    </div>
  );
}
