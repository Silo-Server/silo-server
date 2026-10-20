import { useId, useRef, useState } from "react";
import { ImagePlus, Trash2 } from "lucide-react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useDeleteOverlayImage, useUploadOverlayImage } from "@/hooks/queries/admin/overlayImages";
import type { OverlayImage } from "@/lib/overlays";

const IMAGE_ACCEPT = "image/png,image/jpeg,image/webp";
const MAX_IMAGE_BYTES = 4 << 20;
const MAX_NAME_LENGTH = 64;

// "4K Dolby Vision.png" → "4K Dolby Vision".
function nameFromFile(file: File): string {
  return file.name.replace(/\.[^.]+$/, "").slice(0, MAX_NAME_LENGTH);
}

/**
 * The server's overlay image library: images an administrator uploads so a
 * badge can show a logo instead of text. Badges pick them from their Adjust
 * menus. The images are the administrator's to license; Silo ships none.
 */
export function OverlayImageLibrary({
  images,
  uploadsAvailable,
}: {
  images: readonly OverlayImage[];
  uploadsAvailable: boolean;
}) {
  const upload = useUploadOverlayImage();
  const remove = useDeleteOverlayImage();
  const fileRef = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<OverlayImage | null>(null);
  const nameId = useId();

  const pick = (file: File | undefined) => {
    setError(null);
    if (!file) return;
    if (file.size > MAX_IMAGE_BYTES) {
      setError("Pick an image of 4 MB or less.");
      return;
    }
    setPending(file);
    setName(nameFromFile(file));
  };

  const submit = () => {
    const trimmed = name.trim();
    if (!pending || trimmed.length === 0) return;
    // The picked file and name stay until the upload succeeds, so a refused
    // one (library full, unreadable file) can be retried as it was.
    upload.mutate(
      { file: pending, name: trimmed },
      {
        onSuccess: () => {
          setPending(null);
          setName("");
          if (fileRef.current) fileRef.current.value = "";
        },
      },
    );
  };

  return (
    <div className="space-y-4">
      {uploadsAvailable ? (
        <>
          <div className="flex flex-wrap items-end gap-3">
            <input
              ref={fileRef}
              type="file"
              accept={IMAGE_ACCEPT}
              className="sr-only"
              aria-label="Badge image file"
              onChange={(event) => pick(event.target.files?.[0])}
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => fileRef.current?.click()}
              disabled={upload.isPending}
            >
              <ImagePlus className="h-4 w-4" aria-hidden="true" />
              {pending ? "Pick another image" : "Choose image"}
            </Button>
            {pending ? (
              <>
                <div className="space-y-1">
                  <label htmlFor={nameId} className="text-muted-foreground text-xs font-medium">
                    Name
                  </label>
                  <Input
                    id={nameId}
                    value={name}
                    maxLength={MAX_NAME_LENGTH}
                    onChange={(event) => setName(event.target.value)}
                    className="h-8 w-56"
                  />
                </div>
                <Button
                  type="button"
                  size="sm"
                  onClick={submit}
                  disabled={name.trim().length === 0 || upload.isPending}
                >
                  Upload
                </Button>
              </>
            ) : null}
          </div>
          {error ? <p className="text-destructive text-sm">{error}</p> : null}
          <p className="text-muted-foreground text-[13px] leading-relaxed">
            PNG, JPEG, or WebP up to 4 MB. A transparent PNG works best. Then pick it from a badge's
            Adjust menu, as its icon or for one value such as 2160p.
          </p>
        </>
      ) : (
        <p className="text-muted-foreground text-sm leading-relaxed">
          Image uploads need asset storage. Configure storage to upload badge images. Images already
          in the library stay listed below and can be deleted.
        </p>
      )}
      {images.length > 0 ? (
        <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4">
          {images.map((image) => (
            <li
              key={image.ref}
              className="border-border/60 flex items-center gap-2 rounded-lg border p-2"
            >
              <span className="flex h-10 w-16 shrink-0 items-center justify-center rounded-md bg-gradient-to-br from-slate-700 to-slate-900 p-1">
                <img src={image.url} alt="" className="max-h-full max-w-full object-contain" />
              </span>
              <span className="min-w-0 flex-1 truncate text-sm" title={image.name}>
                {image.name}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={`Delete ${image.name}`}
                onClick={() => setConfirmDelete(image)}
              >
                <Trash2 className="h-4 w-4" aria-hidden="true" />
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      <ConfirmDialog
        open={confirmDelete !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDelete(null);
        }}
        title="Delete this image?"
        description="Badges that use it go back to showing text, for every profile."
        confirmLabel="Delete"
        variant="destructive"
        isPending={remove.isPending}
        onConfirm={() => {
          if (confirmDelete) remove.mutate({ ref: confirmDelete.ref });
          setConfirmDelete(null);
        }}
      />
    </div>
  );
}
