import { useEffect, useId, useMemo, useRef } from "react";
import { Upload } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { isArtworkStaged } from "@/hooks/queries/collectionScope";
import type { ArtworkDraft, ArtworkSlot, ArtworkSlotDraft } from "@/lib/collections/scope";
import { cn } from "@/lib/utils";

const ACCEPT = "image/jpeg,image/png,image/webp";
const SLOT_LABEL: Record<ArtworkSlot, string> = { poster: "Poster", backdrop: "Backdrop" };

/** An object URL for a chosen file, revoked when the file changes or the slot unmounts. */
function useArtworkDraft(slot: ArtworkSlotDraft | undefined, savedUrl: string | undefined) {
  const file = slot?.file ?? null;
  const fileUrl = useMemo(() => (file ? URL.createObjectURL(file) : undefined), [file]);
  useEffect(
    () => () => {
      if (fileUrl) URL.revokeObjectURL?.(fileUrl);
    },
    [fileUrl],
  );
  const link = slot?.sourceUrl?.trim();
  if (file) return fileUrl;
  if (link) return link;
  return slot?.remove ? undefined : savedUrl;
}

function ArtworkSlotField({
  slot,
  savedUrl,
  value,
  onChange,
  error,
  onRetry,
  disabled,
}: {
  slot: ArtworkSlot;
  savedUrl?: string;
  value?: ArtworkSlotDraft;
  onChange: (next: ArtworkSlotDraft | undefined) => void;
  error?: string;
  onRetry?: () => void;
  disabled?: boolean;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const preview = useArtworkDraft(value, savedUrl);
  const label = SLOT_LABEL[slot];
  const noun = label.toLowerCase();
  const staged = isArtworkStaged(value);
  const pick = () => input.current?.click();

  return (
    <div role="group" aria-labelledby={`${id}-label`} className="grid content-start gap-2">
      <span id={`${id}-label`} className="text-muted-foreground text-[13px]">
        {label}
      </span>
      {preview ? (
        <img
          src={preview}
          alt={`${label} preview`}
          className={cn(
            "bg-muted rounded-[10px] border object-cover",
            slot === "poster" ? "aspect-[2/3] w-[92px]" : "aspect-video w-full",
          )}
        />
      ) : (
        <button
          type="button"
          disabled={disabled}
          onClick={pick}
          className={cn(
            "border-muted-foreground/35 text-muted-foreground hover:border-primary hover:text-foreground grid place-items-center gap-1.5 rounded-[10px] border border-dashed px-2 text-center text-[12.5px] transition-colors",
            slot === "poster" ? "aspect-[2/3] w-[92px]" : "aspect-video w-full",
          )}
        >
          <span className="grid justify-items-center gap-1.5">
            <Upload aria-hidden className="size-4" />
            Upload or paste a link
          </span>
        </button>
      )}
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px]">
        <button
          type="button"
          disabled={disabled}
          className="font-medium underline underline-offset-4"
          onClick={pick}
        >
          {preview ? `Change ${noun}` : `Upload ${noun}`}
        </button>
        {staged ? (
          <>
            <span aria-hidden className="opacity-55">
              ·
            </span>
            <button
              type="button"
              className="font-medium underline underline-offset-4"
              onClick={() => onChange(undefined)}
            >
              Undo
            </button>
          </>
        ) : savedUrl ? (
          <>
            <span aria-hidden className="opacity-55">
              ·
            </span>
            <button
              type="button"
              disabled={disabled}
              className="font-medium underline underline-offset-4"
              onClick={() => onChange({ remove: true })}
            >
              {`Remove ${noun}`}
            </button>
          </>
        ) : null}
      </div>
      <Input
        type="url"
        aria-label={`${label} link`}
        placeholder="Or paste an image link"
        className="h-8 text-[13px]"
        disabled={disabled}
        value={value?.sourceUrl ?? ""}
        onChange={(event) =>
          onChange(
            event.target.value
              ? { sourceUrl: event.target.value }
              : value?.remove
                ? { remove: true }
                : undefined,
          )
        }
      />
      {error ? (
        <p
          role="alert"
          className="text-destructive flex flex-wrap items-center gap-2 text-[12.5px]"
        >
          {`Couldn't save the ${noun}. ${error}`}
          {onRetry ? (
            <Button type="button" size="sm" variant="outline" className="h-7" onClick={onRetry}>
              Retry
            </Button>
          ) : null}
        </p>
      ) : staged ? (
        <p className="text-muted-foreground text-[12.5px]">
          {value?.remove && !value.file && !value.sourceUrl?.trim()
            ? `The ${noun} is removed when you save.`
            : `The new ${noun} saves when you press Save.`}
        </p>
      ) : null}
      <input
        ref={input}
        type="file"
        accept={ACCEPT}
        aria-label={`Upload ${noun}`}
        className="hidden"
        onChange={(event) => {
          const file = event.target.files?.[0];
          if (file) onChange({ file });
          event.target.value = "";
        }}
      />
    </div>
  );
}

/**
 * The collection's artwork slots (server: poster and backdrop; personal:
 * poster). A choice is staged until Save; a slot whose upload failed after the
 * collection saved stays staged and offers Retry.
 */
export function ArtworkFields({
  slots,
  saved,
  value,
  onChange,
  errors,
  onRetry,
  disabled,
}: {
  slots: readonly ArtworkSlot[];
  saved: Partial<Record<ArtworkSlot, string | undefined>>;
  value: ArtworkDraft;
  onChange: (next: ArtworkDraft) => void;
  errors?: Partial<Record<ArtworkSlot, string>>;
  onRetry?: () => void;
  disabled?: boolean;
}) {
  return (
    <div className="grid gap-2">
      <h3 className="text-[14.5px] font-semibold">
        Artwork <span className="text-muted-foreground font-normal">Optional</span>
      </h3>
      <div className={cn("grid gap-4", slots.length > 1 && "grid-cols-[92px_minmax(0,1fr)]")}>
        {slots.map((slot) => (
          <ArtworkSlotField
            key={slot}
            slot={slot}
            savedUrl={saved[slot]}
            value={value[slot]}
            error={errors?.[slot]}
            onRetry={onRetry}
            disabled={disabled}
            onChange={(next) => {
              const { [slot]: _previous, ...rest } = value;
              onChange(next ? { ...rest, [slot]: next } : rest);
            }}
          />
        ))}
      </div>
      {slots.includes("poster") ? (
        <p className="text-muted-foreground text-[12.5px]">
          With no poster, viewers see a collage.
        </p>
      ) : null}
    </div>
  );
}
