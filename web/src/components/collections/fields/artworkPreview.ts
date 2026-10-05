import { useEffect, useMemo } from "react";

import type { ArtworkState } from "@/lib/collections/copy";
import type { ArtworkSlotDraft } from "@/lib/collections/scope";

/** What a slot will show after Save. */
export function artworkState(
  slot: ArtworkSlotDraft | undefined,
  savedUrl: string | undefined,
): ArtworkState {
  if (slot?.file || slot?.sourceUrl?.trim()) return "new";
  return savedUrl && !slot?.remove ? "saved" : "none";
}

/**
 * The image a slot shows: a chosen file (as an object URL, revoked when the
 * file changes or the slot unmounts), a pasted link, or the saved image
 * unless it's being removed.
 */
export function useArtworkPreview(
  slot: ArtworkSlotDraft | undefined,
  savedUrl: string | undefined,
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
  return slot?.remove ? undefined : savedUrl;
}
