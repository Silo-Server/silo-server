import { useId, useRef, useState, type RefObject } from "react";
import { Check, ImageIcon, LayoutGrid, Link2, Pencil, Trash2, Upload } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { isArtworkStaged } from "@/hooks/queries/collectionScope";
import {
  ARTWORK_SLOT_LABEL,
  BACKDROP_CAPTION,
  NO_POSTER,
  POSTER_AWAITS_COLLAGE,
  POSTER_IS_COLLAGE,
  type ArtworkState,
} from "@/lib/collections/copy";
import type { ArtworkDraft, ArtworkSlot, ArtworkSlotDraft } from "@/lib/collections/scope";
import { cn } from "@/lib/utils";

import {
  artworkState,
  NO_FALLBACK,
  useArtworkPreview,
  type ArtworkFallback,
} from "./artworkPreview";

const ACCEPT = "image/jpeg,image/png,image/webp";

/** Under the poster tile, by what it shows; nothing under a chosen or saved image. */
const POSTER_CAPTION: Readonly<Partial<Record<ArtworkState, string>>> = {
  collage: POSTER_IS_COLLAGE,
  "awaiting-collage": POSTER_AWAITS_COLLAGE,
  none: NO_POSTER,
};

/**
 * An empty slot: the collage a poster falls back to (not made yet), or no
 * image at all for a poster that never gets a collage and for a backdrop.
 */
export function EmptyArtwork({
  slot,
  compact = false,
  collages = true,
}: {
  slot: ArtworkSlot;
  compact?: boolean;
  /** A poster without a collage to fall back to shows as no poster. */
  collages?: boolean;
}) {
  const collage = slot === "poster" && collages;
  const Icon = collage ? LayoutGrid : ImageIcon;
  let label = "No backdrop";
  if (collage) label = "Collage";
  else if (slot === "poster") label = NO_POSTER;
  return (
    <span
      className={cn(
        "text-muted-foreground bg-muted/30 grid size-full place-content-center justify-items-center gap-1.5 text-[12px]",
        compact ? "[&_svg]:size-3.5" : "[&_svg]:size-4",
      )}
    >
      <Icon aria-hidden />
      {compact ? null : label}
    </span>
  );
}

const TILE_FRAME =
  "border-border/80 bg-muted/20 relative overflow-hidden rounded-[10px] border shadow-lg";

/**
 * One image as a tile with one pencil button. Its menu uploads a file, opens
 * the link box, or goes back to the collage (poster) or no image. The choice
 * is staged until Save; Undo drops it.
 */
function ArtworkTile({
  slot,
  savedUrl,
  fallback,
  value,
  onChange,
  onPasteLink,
  focusPasteField,
  triggerRef,
  error,
  onRetry,
  disabled,
}: {
  slot: ArtworkSlot;
  /** The image someone chose and saved; never the server's collage. */
  savedUrl?: string;
  fallback: ArtworkFallback;
  value?: ArtworkSlotDraft;
  onChange: (next: ArtworkSlotDraft | undefined) => void;
  onPasteLink: () => void;
  /** Called once the menu has closed after Paste a link…, so focus lands in the link field. */
  focusPasteField: () => void;
  triggerRef: (button: HTMLButtonElement | null) => void;
  error?: string;
  onRetry?: () => void;
  disabled?: boolean;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  // After Paste a link…, focus goes to the link field rather than back to the pencil.
  const pasting = useRef(false);
  const preview = useArtworkPreview(value, savedUrl, fallback);
  const state = artworkState(value, savedUrl, fallback);
  const label = ARTWORK_SLOT_LABEL[slot];
  const noun = label.toLowerCase();
  const poster = slot === "poster";
  const usesCollage = poster && fallback.collages;
  const staged = isArtworkStaged(value);
  // Back to the collage or no image: removes a saved image on Save, or drops a new one.
  const clear = () => onChange(savedUrl ? { remove: true } : undefined);
  const caption = poster ? POSTER_CAPTION[state] : preview ? undefined : BACKDROP_CAPTION;

  return (
    <div
      role="group"
      aria-labelledby={`${id}-label`}
      className={cn("grid content-start gap-2", poster ? "w-28" : "w-full max-w-64")}
    >
      <div className={cn(TILE_FRAME, poster ? "aspect-[2/3]" : "aspect-video")}>
        {preview ? (
          <img src={preview} alt={`${label} preview`} className="size-full object-cover" />
        ) : (
          <EmptyArtwork slot={slot} collages={fallback.collages} />
        )}
        <DropdownMenu>
          <DropdownMenuTrigger asChild disabled={disabled}>
            <Button
              ref={triggerRef}
              type="button"
              size="icon-sm"
              aria-label={`Change ${noun}`}
              className="absolute right-1.5 bottom-1.5 size-7 rounded-lg bg-black/70 text-white ring-1 ring-white/10 backdrop-blur hover:bg-black/85 [&_svg]:size-3.5"
            >
              <Pencil aria-hidden />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="start"
            className="min-w-[220px]"
            onCloseAutoFocus={(event) => {
              if (!pasting.current) return;
              pasting.current = false;
              event.preventDefault();
              focusPasteField();
            }}
          >
            <DropdownMenuItem onSelect={() => input.current?.click()}>
              <Upload aria-hidden />
              Upload image…
            </DropdownMenuItem>
            <DropdownMenuItem
              onSelect={() => {
                pasting.current = true;
                onPasteLink();
              }}
            >
              <Link2 aria-hidden />
              Paste a link…
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={state !== "new" && state !== "saved"} onSelect={clear}>
              {usesCollage ? <LayoutGrid aria-hidden /> : <Trash2 aria-hidden />}
              {usesCollage ? "Use the collage" : `Remove ${noun}`}
              {usesCollage && (state === "collage" || state === "awaiting-collage") ? (
                <Check aria-hidden className="ml-auto" />
              ) : null}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div className="grid gap-0.5">
        <span id={`${id}-label`} className="text-[13px] font-medium">
          {label}
        </span>
        {caption ? (
          <span className="text-muted-foreground text-[12.5px] leading-snug">{caption}</span>
        ) : null}
      </div>
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
        <p className="text-muted-foreground text-[12.5px] leading-snug">
          {value?.remove && !value.file && !value.sourceUrl?.trim()
            ? `The ${noun} is removed when you save.`
            : `The new ${noun} saves when you press Save.`}{" "}
          <button
            type="button"
            className="text-foreground font-medium underline underline-offset-4"
            onClick={() => onChange(undefined)}
          >
            Undo
          </button>
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

/** "Paste a link…": the link field, shown only after that choice. */
function PasteLinkBox({
  slot,
  initial,
  fieldRef,
  onUse,
  onClose,
}: {
  slot: ArtworkSlot;
  initial: string;
  fieldRef: RefObject<HTMLInputElement | null>;
  onUse: (link: string) => void;
  onClose: () => void;
}) {
  const id = useId();
  const [link, setLink] = useState(initial);
  const use = () => {
    if (link.trim()) onUse(link.trim());
  };
  return (
    <div className="border-border bg-popover grid max-w-[360px] gap-2.5 rounded-xl border p-3.5">
      <label htmlFor={`${id}-link`} className="text-[13.5px] font-semibold">
        {`${ARTWORK_SLOT_LABEL[slot]} image link`}
      </label>
      <Input
        ref={fieldRef}
        id={`${id}-link`}
        type="url"
        placeholder="https://"
        value={link}
        onChange={(event) => setLink(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            use();
          } else if (event.key === "Escape") {
            event.preventDefault();
            onClose();
          }
        }}
      />
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onClose}>
          Cancel
        </Button>
        <Button type="button" size="sm" disabled={!link.trim()} onClick={use}>
          Use link
        </Button>
      </div>
    </div>
  );
}

/**
 * The collection's artwork tiles (server: poster and backdrop; personal:
 * poster). A choice is staged until Save; a slot whose upload failed after
 * the collection saved stays staged and offers Retry.
 */
export function ArtworkFields({
  slots,
  saved,
  posterFallback = { collages: true },
  value,
  onChange,
  errors,
  onRetry,
  disabled,
}: {
  slots: readonly ArtworkSlot[];
  /** The images someone chose and saved, never the server's collage. */
  saved: Partial<Record<ArtworkSlot, string | undefined>>;
  /** What the poster shows without an image of its own. */
  posterFallback?: ArtworkFallback;
  value: ArtworkDraft;
  onChange: (next: ArtworkDraft) => void;
  errors?: Partial<Record<ArtworkSlot, string>>;
  onRetry?: () => void;
  disabled?: boolean;
}) {
  const [pasting, setPasting] = useState<ArtworkSlot | null>(null);
  const triggers = useRef<Partial<Record<ArtworkSlot, HTMLButtonElement | null>>>({});
  const pasteField = useRef<HTMLInputElement>(null);
  const setSlot = (slot: ArtworkSlot, next: ArtworkSlotDraft | undefined) => {
    const { [slot]: _previous, ...rest } = value;
    onChange(next ? { ...rest, [slot]: next } : rest);
  };
  const closePaste = () => {
    if (pasting) triggers.current[pasting]?.focus();
    setPasting(null);
  };

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-start gap-5">
        {slots.map((slot) => (
          <ArtworkTile
            key={slot}
            slot={slot}
            savedUrl={saved[slot]}
            fallback={slot === "poster" ? posterFallback : NO_FALLBACK}
            value={value[slot]}
            error={errors?.[slot]}
            onRetry={onRetry}
            disabled={disabled}
            triggerRef={(button) => {
              triggers.current[slot] = button;
            }}
            onPasteLink={() => setPasting(slot)}
            focusPasteField={() => pasteField.current?.focus()}
            onChange={(next) => setSlot(slot, next)}
          />
        ))}
      </div>
      {pasting ? (
        <PasteLinkBox
          key={pasting}
          slot={pasting}
          initial={value[pasting]?.sourceUrl ?? ""}
          fieldRef={pasteField}
          onClose={closePaste}
          onUse={(sourceUrl) => {
            setSlot(pasting, { sourceUrl });
            closePaste();
          }}
        />
      ) : null}
    </div>
  );
}
