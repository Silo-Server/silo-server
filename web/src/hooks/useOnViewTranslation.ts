import { useCallback, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { ItemDetail } from "@/api/types";
import { useMetadataAIStatus } from "@/hooks/queries/items";

// Give up shimmering after this long; the original text stays and the
// server-side cooldown keeps a failing endpoint from being re-hit per view.
const TRANSLATE_TIMEOUT_MS = 45_000;
const POLL_INTERVAL_MS = 2_000;

/**
 * Viewer-facing on-demand description translation for the detail page.
 *
 * When the detail response carries `pending_translation_language` (the
 * description is not in this profile's language and no localization exists),
 * the server's `metadata_ai.on_view` mode decides the UX:
 *   - "auto": fire the translation on view (once per item+language) and pulse
 *     the description until the refetched detail comes back localized.
 *   - "button": expose a translate trigger for the chip under the overview.
 * Completion is observed as the flag clearing on refetch — the job's first
 * batch translates the item's own overview, so this lands in seconds.
 */
export function useOnViewTranslation(item: ItemDetail | undefined) {
  const queryClient = useQueryClient();
  const { data: status } = useMetadataAIStatus();
  const mode = status?.on_view ?? "off";

  const contentId = item?.content_id ?? "";
  const pendingLanguage = item?.pending_translation_language ?? "";
  const seriesId = item?.series_id;
  const seasonNumber = item?.season_number;

  const [translating, setTranslating] = useState(false);
  // Tracks which item+language we already fired for, so auto mode triggers
  // once per view rather than re-firing on every refetch while polling.
  const firedForRef = useRef("");

  const trigger = useCallback(() => {
    if (!contentId || !pendingLanguage) return;
    const key = `${contentId}:${pendingLanguage}`;
    if (firedForRef.current === key) return;
    firedForRef.current = key;
    setTranslating(true);
    api(`/items/${encodeURIComponent(contentId)}/translate-description`, {
      method: "POST",
      body: JSON.stringify({ target_language: pendingLanguage }),
    }).catch(() => {
      setTranslating(false);
    });
  }, [contentId, pendingLanguage]);

  // Auto mode: translate on view.
  useEffect(() => {
    if (mode === "auto" && pendingLanguage) trigger();
  }, [mode, pendingLanguage, trigger]);

  // While translating, poll the detail; the localized overview replaces the
  // text and clears the pending flag, which ends the shimmer below.
  useEffect(() => {
    if (!translating || !contentId) return;
    const startedAt = Date.now();
    const timer = setInterval(() => {
      if (Date.now() - startedAt > TRANSLATE_TIMEOUT_MS) {
        setTranslating(false);
        return;
      }
      // Prefix invalidation covers the per-library detail key variants
      // (["catalog", "items", id, "detail", <libraryId|"default">]).
      void queryClient.invalidateQueries({ queryKey: ["catalog", "items", contentId] });
      if (seriesId && typeof seasonNumber === "number") {
        void queryClient.invalidateQueries({
          queryKey: ["catalog", "series", seriesId, "seasons", seasonNumber],
        });
      }
    }, POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [translating, contentId, queryClient, seasonNumber, seriesId]);

  // The refetched detail no longer reports a missing language: done.
  useEffect(() => {
    if (translating && !pendingLanguage) setTranslating(false);
  }, [translating, pendingLanguage]);

  return {
    /** Pulse the description text. */
    translating,
    /** Render the explicit translate chip (button mode only). */
    onTranslate: mode === "button" && pendingLanguage && !translating ? trigger : undefined,
  };
}
