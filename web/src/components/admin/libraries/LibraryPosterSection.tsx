import type { ChangeEvent } from "react";
import { ImageIcon, Trash2 } from "lucide-react";

import type { Library } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { useDeleteLibraryPoster, useUploadLibraryPoster } from "@/hooks/queries/admin/libraries";

export function LibraryPosterSection({ library }: { library: Library }) {
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
      <Label>Poster</Label>
      <div className="flex items-center gap-2">
        {library.poster_url ? (
          <img
            src={library.poster_url}
            alt={`${library.name} poster`}
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
          className="h-8 text-xs"
          onClick={() => document.getElementById(fileInputId)?.click()}
          disabled={uploadMutation.isPending}
        >
          {uploadMutation.isPending ? "..." : library.poster_url ? "Replace" : "Upload"}
        </Button>
        {library.poster_url && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="text-muted-foreground hover:text-destructive h-8 w-8"
            onClick={() => deleteMutation.mutate(library.id)}
            disabled={deleteMutation.isPending}
            title="Remove poster"
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        )}
      </div>
    </div>
  );
}
