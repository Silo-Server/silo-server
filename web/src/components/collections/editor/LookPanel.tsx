import { useEffect, useId, useRef, useState, type ComponentProps } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";

import { Button } from "@/components/ui/button";
import { lookSummary } from "@/lib/collections/copy";
import type { ArtworkSlot, ArtworkSlotDraft } from "@/lib/collections/scope";
import { cn } from "@/lib/utils";

import { ArtworkFields, EmptyArtwork } from "../fields/ArtworkFields";
import {
  artworkState,
  NO_FALLBACK,
  useArtworkPreview,
  type ArtworkFallback,
} from "../fields/artworkPreview";

/** A small poster or backdrop for the closed card. */
function Thumb({
  slot,
  value,
  savedUrl,
  fallback,
}: {
  slot: ArtworkSlot;
  value?: ArtworkSlotDraft;
  savedUrl?: string;
  fallback: ArtworkFallback;
}) {
  const preview = useArtworkPreview(value, savedUrl, fallback);
  return (
    <span
      className={cn(
        "border-border/80 bg-muted/20 block overflow-hidden rounded-md border",
        slot === "poster" ? "aspect-[2/3] w-8" : "aspect-video w-20 max-sm:hidden",
      )}
    >
      {preview ? (
        <img src={preview} alt="" className="size-full object-cover" />
      ) : (
        <EmptyArtwork slot={slot} compact collages={fallback.collages} />
      )}
    </span>
  );
}

/**
 * The collection's artwork, last on the page. Closed, it is one line: small
 * thumbnails and what each slot shows ("Poster: a collage of its titles ·
 * Backdrop: none"). Open, it shows the artwork tiles. It opens by itself when
 * an image couldn't be saved, so Retry is in sight.
 */
export function LookPanel(props: ComponentProps<typeof ArtworkFields>) {
  const { slots, saved, value, errors, posterFallback = { collages: true } } = props;
  const fallbackOf = (slot: ArtworkSlot) => (slot === "poster" ? posterFallback : NO_FALLBACK);
  const id = useId();
  const [open, setOpen] = useState(false);
  const failed = slots.filter((slot) => errors?.[slot]).join();
  const [shownFailed, setShownFailed] = useState(failed);
  if (failed !== shownFailed) {
    setShownFailed(failed);
    if (failed) setOpen(true);
  }
  // Opening and closing swap the button under focus; carry focus across.
  const toggled = useRef(false);
  const openButton = useRef<HTMLButtonElement>(null);
  const doneButton = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!toggled.current) return;
    toggled.current = false;
    (open ? doneButton : openButton).current?.focus();
  }, [open]);
  const toggle = (next: boolean) => {
    toggled.current = true;
    setOpen(next);
  };

  const summary = lookSummary(
    slots.map((slot) => [slot, artworkState(value[slot], saved[slot], fallbackOf(slot))] as const),
  );

  return (
    <section aria-labelledby={`${id}-heading`} className="surface-panel rounded-[22px]">
      {open ? (
        <div id={`${id}-body`} className="grid gap-4 p-5 sm:p-6">
          <div className="flex items-center justify-between gap-3">
            <h2 id={`${id}-heading`} className="text-[17px] font-semibold">
              Look
            </h2>
            <Button
              ref={doneButton}
              type="button"
              variant="ghost"
              size="sm"
              aria-expanded
              aria-controls={`${id}-body`}
              onClick={() => toggle(false)}
            >
              Done
              <ChevronUp aria-hidden className="opacity-70" />
            </Button>
          </div>
          <ArtworkFields {...props} />
        </div>
      ) : (
        <button
          ref={openButton}
          type="button"
          aria-expanded={false}
          aria-labelledby={`${id}-heading`}
          aria-describedby={`${id}-summary`}
          onClick={() => toggle(true)}
          className="hover:bg-accent/30 focus-visible:ring-ring/50 grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-4 rounded-[22px] px-4 py-4 text-left outline-none focus-visible:ring-[3px] max-sm:grid-cols-[auto_minmax(0,1fr)] sm:pr-6"
        >
          <span className="flex items-center gap-2">
            {slots.map((slot) => (
              <Thumb
                key={slot}
                slot={slot}
                value={value[slot]}
                savedUrl={saved[slot]}
                fallback={fallbackOf(slot)}
              />
            ))}
          </span>
          <span className="grid min-w-0 gap-0.5">
            <span id={`${id}-heading`} className="text-[15px] font-semibold">
              Look
            </span>
            <span id={`${id}-summary`} className="text-muted-foreground text-[13px]">
              {summary}
            </span>
          </span>
          <span
            aria-hidden
            className="border-border inline-flex h-8 items-center gap-1.5 rounded-[10px] border px-3 text-[13.5px] font-medium max-sm:hidden"
          >
            Change
            <ChevronDown className="size-3.5 opacity-70" />
          </span>
        </button>
      )}
    </section>
  );
}
