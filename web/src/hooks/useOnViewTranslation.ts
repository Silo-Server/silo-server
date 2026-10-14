import { useCallback, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import type { ItemDetail } from "@/api/types";
import { useMetadataAIStatus } from "@/hooks/queries/metadataAI";

// Give up shimmering after this long; the original text stays and the
// server-side cooldown keeps a failing endpoint from being re-hit per view.
const TRANSLATE_TIMEOUT_MS = 45_000;
const POLL_INTERVAL_MS = 2_000;
// A season job can outlast the shimmer. After it, keep refreshing slowly for
// a while so episodes that land later still reach the open page.
const SLOW_POLL_INTERVAL_MS = 10_000;
const SLOW_POLL_LIMIT_MS = 5 * 60_000;

/** What the hook needs from a page: a detail document, or a season's episode list. */
export type OnViewTranslationTarget = Pick<
  ItemDetail,
  "content_id" | "pending_translation_language" | "series_id" | "season_number"
>;

/**
 * Viewer-facing on-demand description translation for the detail page.
 *
 * When the detail response carries `pending_translation_language` (the
 * description is not in this profile's language and no localization exists),
 * the server's `metadata_ai.on_view` mode decides the UX:
 *   - "auto": fire the translation on view (once per item+language) and pulse
 *     the description until the refetched detail comes back localized.
 *   - "button": expose a translate trigger for the chip under the overview.
 * Completion is observed as the flag clearing on refetch. An item's own
 * overview lands in seconds; a season's flag clears only once its episodes
 * are translated too, which can outlast the wait.
 */
export function useOnViewTranslation(item: OnViewTranslationTarget | undefined) {
  const queryClient = useQueryClient();
  const { data: status } = useMetadataAIStatus();
  const mode = status?.on_view ?? "off";

  const contentId = item?.content_id ?? "";
  const pendingLanguage = item?.pending_translation_language ?? "";
  const seriesId = item?.series_id;
  const seasonNumber = item?.season_number;

  const [translating, setTranslating] = useState(false);
  const [settling, setSettling] = useState(false);
  // The last request failed (refused, rate limited, or unreachable). Auto mode
  // fires once per view, so it then offers the translate action instead.
  const [failed, setFailed] = useState(false);
  // Tracks which item+language we already fired for, so auto mode triggers
  // once per view rather than re-firing on every refetch while polling.
  const firedForRef = useRef("");
  // Counts requests, so a late failure of one that a retry replaced cannot
  // end the retry's wait.
  const attemptRef = useRef(0);

  const refresh = useCallback(() => {
    // Prefix invalidation covers the per-library detail key variants
    // (["catalog", "items", id, "detail", <libraryId|"default">]).
    void queryClient.invalidateQueries({ queryKey: ["catalog", "items", contentId] });
    if (seriesId && typeof seasonNumber === "number") {
      void queryClient.invalidateQueries({
        queryKey: ["catalog", "series", seriesId, "seasons", seasonNumber],
      });
    }
  }, [queryClient, contentId, seriesId, seasonNumber]);

  const trigger = useCallback(() => {
    if (!contentId || !pendingLanguage) return;
    const key = `${contentId}:${pendingLanguage}`;
    if (firedForRef.current === key) return;
    firedForRef.current = key;
    const attempt = ++attemptRef.current;
    setSettling(false);
    setFailed(false);
    setTranslating(true);
    v2("POST /api/v2/catalog/items/{id}/translate-description", {
      path: { id: contentId },
      body: { target_language: pendingLanguage },
    }).catch(() => {
      if (attempt !== attemptRef.current) return;
      // The server refuses a language the page no longer reports missing,
      // which happens when another tab or profile finished the translation
      // first. Re-read the page, and let the translate action try again.
      firedForRef.current = "";
      setTranslating(false);
      setFailed(true);
      refresh();
    });
  }, [contentId, pendingLanguage, refresh]);

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
        // A season job can outlast the wait. Release the key so the
        // translate action works again: a retry rejoins the running job.
        firedForRef.current = "";
        setTranslating(false);
        setSettling(true);
        return;
      }
      refresh();
    }, POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [translating, contentId, refresh]);

  // After the shimmer, keep refreshing slowly while the language is still
  // pending, so a long job's later batches show up without a reload.
  useEffect(() => {
    if (!settling || !contentId || !pendingLanguage) return;
    const startedAt = Date.now();
    const timer = setInterval(() => {
      if (Date.now() - startedAt > SLOW_POLL_LIMIT_MS) {
        setSettling(false);
        return;
      }
      refresh();
    }, SLOW_POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [settling, contentId, pendingLanguage, refresh]);

  // The refetched detail no longer reports a missing language: done.
  useEffect(() => {
    if (pendingLanguage) return;
    if (translating) setTranslating(false);
    if (settling) setSettling(false);
  }, [translating, settling, pendingLanguage]);

  return {
    /** Pulse the description text. */
    translating,
    /** Render the explicit translate chip: in button mode, or after a failed auto run. */
    onTranslate:
      (mode === "button" || (mode === "auto" && failed)) && pendingLanguage && !translating
        ? trigger
        : undefined,
  };
}
