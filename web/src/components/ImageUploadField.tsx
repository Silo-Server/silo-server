import { useCallback, useRef } from "react";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useT } from "@/i18n";
import { Upload, X } from "lucide-react";

interface ImageUploadFieldProps {
  label: string;
  currentUrl?: string;
  file: File | null;
  onFileChange: (file: File | null) => void;
  onDelete?: () => void;
  sourceUrl?: string;
  onSourceUrlChange?: (value: string) => void;
  sourceUrlPlaceholder?: string;
}

const ACCEPT = "image/jpeg,image/png,image/webp";

export function ImageUploadField({
  label,
  currentUrl,
  file,
  onFileChange,
  onDelete,
  sourceUrl,
  onSourceUrlChange,
  sourceUrlPlaceholder = "https://example.com/image.jpg",
}: ImageUploadFieldProps) {
  const t = useT();
  const inputRef = useRef<HTMLInputElement>(null);

  const previewUrl = file ? URL.createObjectURL(file) : currentUrl;

  const handleDrop = useCallback(
    (event: React.DragEvent) => {
      event.preventDefault();
      const dropped = event.dataTransfer.files[0];
      if (dropped && ACCEPT.split(",").includes(dropped.type)) {
        onFileChange(dropped);
      }
    },
    [onFileChange],
  );

  const handleDragOver = useCallback((event: React.DragEvent) => {
    event.preventDefault();
  }, []);

  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      {previewUrl ? (
        <div className="relative">
          <img
            src={previewUrl}
            alt={label}
            className="bg-muted h-32 w-full rounded-lg border object-cover"
          />
          <div className="absolute top-1 right-1 flex gap-1">
            {file && (
              <Button
                type="button"
                variant="secondary"
                size="icon"
                className="h-6 w-6"
                onClick={() => onFileChange(null)}
                title={t("web.common.ui.imageUpload.removeSelected")}
              >
                <X className="h-3 w-3" />
              </Button>
            )}
            {!file && currentUrl && onDelete && (
              <Button
                type="button"
                variant="destructive"
                size="icon"
                className="h-6 w-6"
                onClick={onDelete}
                title={t("web.common.ui.imageUpload.delete")}
              >
                <X className="h-3 w-3" />
              </Button>
            )}
          </div>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          onDrop={handleDrop}
          onDragOver={handleDragOver}
          className="border-border text-muted-foreground hover:border-primary hover:bg-accent flex h-32 w-full flex-col items-center justify-center gap-2 rounded-lg border-2 border-dashed transition-colors"
        >
          <Upload className="h-6 w-6" />
          <span className="text-xs">{t("web.common.ui.imageUpload.drop")}</span>
        </button>
      )}
      <input
        ref={inputRef}
        type="file"
        accept={ACCEPT}
        className="hidden"
        onChange={(event) => {
          const selected = event.target.files?.[0];
          if (selected) onFileChange(selected);
          event.target.value = "";
        }}
      />
      {onSourceUrlChange ? (
        <div className="space-y-2">
          <Input
            type="url"
            value={sourceUrl ?? ""}
            onChange={(event) => onSourceUrlChange(event.target.value)}
            placeholder={sourceUrlPlaceholder}
          />
          <p className="text-muted-foreground text-xs">{t("web.common.ui.imageUpload.urlHelp")}</p>
        </div>
      ) : null}
    </div>
  );
}
