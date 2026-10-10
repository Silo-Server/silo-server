import { useCallback, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import type { ItemDetail } from "@/api/types";
import { useMetadataAIStatus } from "@/hooks/queries/metadataAI";

// Give up shimmering after this long; the original text stays and the
// server-side cooldown keeps a failing endpoint from being re-hit per view.
const TRANSLATE_TIMEOUT_MS = 45_000;
const POLL_INTERVAL_MS = 2_000;

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
 * Completion is observed as the flag clearing on refetch — the job's first
 * batch translates the item's own overview, so this lands in seconds.
 */
export interface OnViewTranslationOptions {
  /**
   * Called on every poll tick while a translation runs, for surfaces whose
   * data does not live under the catalog item keys (Home sections).
   */
  onPoll?: () => void;
}

export function useOnViewTranslation(
  item: OnViewTranslationTarget | undefined,
  { onPoll }: OnViewTranslationOptions = {},
) {
  const queryClient = useQueryClient();
  const { data: status } = useMetadataAIStatus();
  const mode = status?.on_view ?? "off";

  const contentId = item?.content_id ?? "";
  const pendingLanguage = item?.pending_translation_language ?? "";
  const seriesId = item?.series_id;
  const seasonNumber = item?.season_number;
  const key = contentId && pendingLanguage ? `${contentId}\u0000${pendingLanguage}` : "";

  // Translations in flight, keyed by item and language. A surface that
  // switches items (the Featured hero rotating its slides) shows each item's
  // own progress and keeps polling for an earlier slide's job.
  const [running, setRunning] = useState<ReadonlyMap<string, RunningTranslation>>(new Map());
  const translating = key !== "" && running.has(key);
  // Item+language pairs already requested, so auto mode triggers once per
  // view rather than re-firing on every refetch while polling.
  const firedRef = useRef(new Set<string>());
  const onPollRef = useRef(onPoll);
  useEffect(() => {
    onPollRef.current = onPoll;
  });

  const trigger = useCallback(() => {
    if (!key || firedRef.current.has(key)) return;
    firedRef.current.add(key);
    setRunning((current) => new Map(current).set(key, { contentId, startedAt: Date.now() }));
    v2("POST /api/v2/catalog/items/{id}/translate-description", {
      path: { id: contentId },
      body: { target_language: pendingLanguage },
    }).catch(() => {
      setRunning((current) => withoutEntries(current, (entryKey) => entryKey === key));
    });
  }, [contentId, key, pendingLanguage]);

  // Auto mode: translate on view.
  useEffect(() => {
    if (mode === "auto" && pendingLanguage) trigger();
  }, [mode, pendingLanguage, trigger]);

  // While translating, poll; the localized overview replaces the text and
  // clears the pending flag, which ends the shimmer below.
  useEffect(() => {
    if (running.size === 0) return;
    const timer = setInterval(() => {
      const now = Date.now();
      if ([...running.values()].some((entry) => now - entry.startedAt > TRANSLATE_TIMEOUT_MS)) {
        setRunning((current) =>
          withoutEntries(current, (_, entry) => now - entry.startedAt > TRANSLATE_TIMEOUT_MS),
        );
      }
      // Prefix invalidation covers the per-library detail key variants
      // (["catalog", "items", id, "detail", <libraryId|"default">]).
      for (const id of new Set([...running.values()].map((entry) => entry.contentId))) {
        void queryClient.invalidateQueries({ queryKey: ["catalog", "items", id] });
      }
      if (seriesId && typeof seasonNumber === "number") {
        void queryClient.invalidateQueries({
          queryKey: ["catalog", "series", seriesId, "seasons", seasonNumber],
        });
      }
      onPollRef.current?.();
    }, POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [running, queryClient, seasonNumber, seriesId]);

  // The refetched item no longer reports a missing language: done.
  useEffect(() => {
    if (!contentId || pendingLanguage) return;
    setRunning((current) => withoutEntries(current, (_, entry) => entry.contentId === contentId));
  }, [contentId, pendingLanguage]);

  return {
    /** Pulse the description text. */
    translating,
    /** Render the explicit translate chip (button mode only). */
    onTranslate: mode === "button" && pendingLanguage && !translating ? trigger : undefined,
  };
}

interface RunningTranslation {
  contentId: string;
  startedAt: number;
}

/** The map without matching entries, or the same map when nothing matches. */
function withoutEntries(
  current: ReadonlyMap<string, RunningTranslation>,
  drop: (key: string, entry: RunningTranslation) => boolean,
): ReadonlyMap<string, RunningTranslation> {
  const next = new Map([...current].filter(([key, entry]) => !drop(key, entry)));
  return next.size === current.size ? current : next;
}
