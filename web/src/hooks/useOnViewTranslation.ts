import { useCallback, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import type { ItemDetail } from "@/api/types";
import { sectionKeys } from "@/hooks/queries/keys";
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

export interface OnViewTranslationOptions {
  /**
   * Called on every poll tick while a translation runs, for surfaces whose
   * data does not live under the catalog item keys (Home sections).
   */
  onPoll?: () => void;
  /**
   * The content IDs that still lack the language on a surface that switches
   * items (the Featured hero's slides). A running translation whose item has
   * left this set finished off screen, so it stops polling.
   */
  pendingContentIds?: ReadonlySet<string>;
}

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
export function useOnViewTranslation(
  item: OnViewTranslationTarget | undefined,
  { onPoll, pendingContentIds }: OnViewTranslationOptions = {},
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
  // An entry past the wait stops pulsing but stays, settling, while the
  // language is still pending.
  const [running, setRunning] = useState<ReadonlyMap<string, RunningTranslation>>(new Map());
  const entry = key ? running.get(key) : undefined;
  const translating = entry !== undefined && entry.settlingSince === undefined;
  // Item+language pairs already requested, so auto mode triggers once per
  // view rather than re-firing on every refetch while polling. A failure or a
  // timeout releases the pair, and the button bypasses this, so a viewer can
  // retry.
  const firedRef = useRef(new Set<string>());
  // The latest request per item+language, so a late failure of one that a
  // retry replaced cannot end the retry's wait.
  const attemptsRef = useRef(new Map<string, number>());
  // Item+language pairs whose last request failed (refused, rate limited, or
  // unreachable). Auto mode fires once per view, so it then offers the
  // translate action instead.
  const [failedKeys, setFailedKeys] = useState<ReadonlySet<string>>(new Set());
  const onPollRef = useRef(onPoll);
  useEffect(() => {
    onPollRef.current = onPoll;
  });

  const trigger = useCallback(
    (manual: boolean) => {
      if (!key || (!manual && firedRef.current.has(key))) return;
      firedRef.current.add(key);
      const attempt = (attemptsRef.current.get(key) ?? 0) + 1;
      attemptsRef.current.set(key, attempt);
      setFailedKeys((current) => withoutKey(current, key));
      setRunning((current) =>
        new Map(current).set(key, { contentId, seriesId, seasonNumber, startedAt: Date.now() }),
      );
      v2("POST /api/v2/catalog/items/{id}/translate-description", {
        path: { id: contentId },
        body: { target_language: pendingLanguage },
      }).catch(() => {
        if (attemptsRef.current.get(key) !== attempt) return;
        firedRef.current.delete(key);
        setRunning((current) => withoutEntries(current, (entryKey) => entryKey === key));
        setFailedKeys((current) => new Set(current).add(key));
        // The server refuses a language the page no longer reports missing,
        // which happens when another tab or profile finished the translation
        // first. Re-read the page, and let the translate action try again.
        void queryClient.invalidateQueries({ queryKey: ["catalog", "items", contentId] });
        if (seriesId && typeof seasonNumber === "number") {
          void queryClient.invalidateQueries({
            queryKey: ["catalog", "series", seriesId, "seasons", seasonNumber],
          });
        }
        onPollRef.current?.();
      });
    },
    [contentId, key, pendingLanguage, seriesId, seasonNumber, queryClient],
  );
  const translateNow = useCallback(() => trigger(true), [trigger]);

  // Auto mode: translate on view.
  useEffect(() => {
    if (mode === "auto" && pendingLanguage) trigger(false);
  }, [mode, pendingLanguage, trigger]);

  // While translating, poll; the localized overview replaces the text and
  // clears the pending flag, which ends the shimmer below. Once every entry is
  // settling, poll slowly instead.
  const allSettling = [...running.values()].every((entry) => entry.settlingSince !== undefined);
  useEffect(() => {
    if (running.size === 0) return;
    const timer = setInterval(
      () => {
        const now = Date.now();
        // Read the entries from the update, not this render: ticks can run
        // before the next render replaces the interval.
        setRunning((current) => {
          let changed = false;
          const next = new Map<string, RunningTranslation>();
          for (const [entryKey, entry] of current) {
            if (entry.settlingSince === undefined && now - entry.startedAt > TRANSLATE_TIMEOUT_MS) {
              // A season job can outlast the wait. Release the pair so the
              // translate action works again: a retry rejoins the running
              // job. Auto mode does not re-fire until the item is viewed again.
              firedRef.current.delete(entryKey);
              next.set(entryKey, { ...entry, settlingSince: now });
              changed = true;
            } else if (
              entry.settlingSince !== undefined &&
              now - entry.settlingSince > SLOW_POLL_LIMIT_MS
            ) {
              changed = true;
            } else {
              next.set(entryKey, entry);
            }
          }
          return changed ? next : current;
        });
        // Prefix invalidation covers the per-library detail key variants
        // (["catalog", "items", id, "detail", <libraryId|"default">]).
        // Each entry refreshes its own item and season, not the one on screen.
        const keys = new Map<string, readonly unknown[]>();
        for (const entry of running.values()) {
          keys.set(entry.contentId, ["catalog", "items", entry.contentId]);
          if (entry.seriesId && typeof entry.seasonNumber === "number") {
            const season = ["catalog", "series", entry.seriesId, "seasons", entry.seasonNumber];
            keys.set(season.join("\u0000"), season);
          }
        }
        for (const queryKey of keys.values()) void queryClient.invalidateQueries({ queryKey });
        onPollRef.current?.();
      },
      allSettling ? SLOW_POLL_INTERVAL_MS : POLL_INTERVAL_MS,
    );
    return () => clearInterval(timer);
  }, [running, allSettling, queryClient]);

  // Home and library rows carry the same localized text. Mark them stale when
  // a translation lands so the next visit shows it; rows on screen keep theirs.
  const markSectionRowsStale = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: sectionKeys.home(), refetchType: "none" });
    void queryClient.invalidateQueries({
      queryKey: sectionKeys.libraryRoot(),
      refetchType: "none",
    });
  }, [queryClient]);

  // The refetched item no longer reports a missing language: done.
  const finished =
    contentId !== "" &&
    !pendingLanguage &&
    [...running.values()].some((runningEntry) => runningEntry.contentId === contentId);
  useEffect(() => {
    if (!finished) return;
    setRunning((current) => withoutEntries(current, (_, entry) => entry.contentId === contentId));
    markSectionRowsStale();
  }, [finished, contentId, markSectionRowsStale]);

  // A page that shows one item (a detail page) follows only that item: moving
  // to another item leaves the earlier job behind instead of polling for it.
  useEffect(() => {
    if (pendingContentIds) return;
    setRunning((current) =>
      withoutEntries(current, (entryKey, entry) => {
        if (entry.contentId === contentId) return false;
        // Returning to the item later starts its job again.
        firedRef.current.delete(entryKey);
        return true;
      }),
    );
  }, [pendingContentIds, contentId]);

  // An item the surface no longer reports as pending is done, even off screen.
  const finishedOffScreen =
    pendingContentIds !== undefined &&
    [...running.values()].some((runningEntry) => !pendingContentIds.has(runningEntry.contentId));
  useEffect(() => {
    if (!finishedOffScreen || !pendingContentIds) return;
    setRunning((current) =>
      withoutEntries(current, (_, entry) => !pendingContentIds.has(entry.contentId)),
    );
    markSectionRowsStale();
  }, [finishedOffScreen, pendingContentIds, markSectionRowsStale]);

  return {
    /** Pulse the description text. */
    translating,
    /** Render the explicit translate chip: in button mode, or after a failed auto run. */
    onTranslate:
      (mode === "button" || (mode === "auto" && failedKeys.has(key))) &&
      pendingLanguage &&
      !translating
        ? translateNow
        : undefined,
  };
}

interface RunningTranslation {
  contentId: string;
  seriesId?: string | null;
  seasonNumber?: number | null;
  startedAt: number;
  /** When the wait ran out; the entry then refreshes slowly until the limit. */
  settlingSince?: number;
}

/** The map without matching entries, or the same map when nothing matches. */
function withoutEntries(
  current: ReadonlyMap<string, RunningTranslation>,
  drop: (key: string, entry: RunningTranslation) => boolean,
): ReadonlyMap<string, RunningTranslation> {
  const next = new Map([...current].filter(([key, entry]) => !drop(key, entry)));
  return next.size === current.size ? current : next;
}

/** The set without key, or the same set when it has no key. */
function withoutKey(current: ReadonlySet<string>, key: string): ReadonlySet<string> {
  if (!current.has(key)) return current;
  const next = new Set(current);
  next.delete(key);
  return next;
}
