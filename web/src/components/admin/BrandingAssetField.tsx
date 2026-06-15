import { useRef } from "react";
import { ImageOff, Loader2, Trash2, Upload } from "lucide-react";

import { cn } from "@/lib/utils";
import {
  useDeleteBrandingAsset,
  useUploadBrandingAsset,
  type BrandingAssetKind,
} from "@/hooks/queries/admin/branding";

interface BrandingAssetFieldProps {
  label: string;
  description?: string;
  kind: BrandingAssetKind;
  /** Current asset URL (from the branding read), or null when using the default. */
  currentUrl: string | null;
  /** Accepted file types for the file input. */
  accept: string;
  /** When false, uploads are blocked (e.g. S3 not configured). */
  enabled: boolean;
  /** Square preview suits the icon/favicon; wide suits the wordmark/background. */
  preview?: "square" | "wide";
}

export function BrandingAssetField({
  label,
  description,
  kind,
  currentUrl,
  accept,
  enabled,
  preview = "wide",
}: BrandingAssetFieldProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const upload = useUploadBrandingAsset();
  const remove = useDeleteBrandingAsset();
  const busy = upload.isPending || remove.isPending;

  const handleFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = ""; // allow re-selecting the same file
    if (file) {
      upload.mutate({ kind, file });
    }
  };

  return (
    <div className="border-border bg-background flex items-center gap-4 rounded-xl border p-3">
      <div
        className={cn(
          "bg-muted/40 border-border flex shrink-0 items-center justify-center overflow-hidden rounded-lg border",
          preview === "square" ? "h-14 w-14" : "h-14 w-28",
        )}
      >
        {currentUrl ? (
          <img src={currentUrl} alt={`${label} preview`} className="h-full w-full object-contain" />
        ) : (
          <ImageOff className="text-muted-foreground h-5 w-5" />
        )}
      </div>

      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{label}</p>
        {description && <p className="text-muted-foreground mt-0.5 text-xs">{description}</p>}
      </div>

      <input
        ref={inputRef}
        type="file"
        accept={accept}
        className="hidden"
        onChange={handleFile}
        disabled={!enabled || busy}
      />
      <div className="flex shrink-0 items-center gap-2">
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          disabled={!enabled || busy}
          className="border-border hover:bg-muted/50 inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50"
        >
          {upload.isPending ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Upload className="h-3.5 w-3.5" />
          )}
          {currentUrl ? "Replace" : "Upload"}
        </button>
        {currentUrl && (
          <button
            type="button"
            onClick={() => remove.mutate({ kind })}
            disabled={busy}
            aria-label={`Remove ${label}`}
            className="text-muted-foreground hover:text-destructive inline-flex items-center rounded-lg border border-transparent p-1.5 transition-colors disabled:opacity-50"
          >
            {remove.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Trash2 className="h-3.5 w-3.5" />
            )}
          </button>
        )}
      </div>
    </div>
  );
}
