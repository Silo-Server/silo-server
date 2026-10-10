import { useEffect, useMemo } from "react";

import type { ArtworkState } from "@/lib/collections/copy";
import type { ArtworkSlotDraft } from "@/lib/collections/scope";

/**
 * What a poster shows without an image of its own: the collage the server
 * made (`collageUrl`, once built), or nothing when the collection never gets
 * one (`collages: false`, a server Smart collection's poster or any backdrop).
 */
export interface ArtworkFallback {
  collages: boolean;
  collageUrl?: string;
}

export const NO_FALLBACK: ArtworkFallback = { collages: false };

/** What a slot will show after Save. */
export function artworkState(
  slot: ArtworkSlotDraft | undefined,
  savedUrl: string | undefined,
  fallback: ArtworkFallback = NO_FALLBACK,
): ArtworkState {
  if (slot?.file || slot?.sourceUrl?.trim()) return "new";
  if (savedUrl && !slot?.remove) return "saved";
  if (!fallback.collages) return "none";
  // A collage stands in only for a poster that isn't being removed: removing
  // an image brings a collage that isn't made yet.
  return fallback.collageUrl && !slot?.remove ? "collage" : "awaiting-collage";
}

/**
 * The image a slot shows: a chosen file (as an object URL, revoked when the
 * file changes or the slot unmounts), a pasted link, the saved image unless
 * it's being removed, or else the server's collage.
 */
export function useArtworkPreview(
  slot: ArtworkSlotDraft | undefined,
  savedUrl: string | undefined,
  fallback: ArtworkFallback = NO_FALLBACK,
) {
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
  if (slot?.remove) return undefined;
  return savedUrl ?? (fallback.collages ? fallback.collageUrl : undefined);
}
