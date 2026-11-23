import { useCallback, useRef, useState } from "react";
import { Link } from "react-router";
import { Pause, Play } from "lucide-react";
import { JellyfinSessionPill } from "@/components/JellyfinSessionPill";
import { PlaybackRouteBadges } from "@/components/PlaybackRouteBadges";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import {
  activityMethodMeta,
  classifyActivityMethod,
  formatPlaybackPosition,
  getPlaybackSessionSubtitle,
  getSessionClientLabel,
  isEpisodeSession,
} from "@/pages/adminActivityPresentation";
import type { AdminSession } from "@/api/types";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import { SectionError } from "../feedback";
import { useReportCollapsed } from "../widgetChrome";
import { SessionProfilePill } from "./SessionProfilePill";
import { useT } from "@/i18n";
import { getInitial } from "@/lib/text";

/**
 * Most cards the widget mounts at once. The widget shows only what fits, and a
 * busy server can hold hundreds of sessions; the activity page lists the rest.
 */
const MAX_RENDERED_STREAMS = 40;

/**
 * Cards shown below the dashboard's 12-column layout, where rows size to their
 * content and there is no widget height to fit against. FIXED_ROWS_QUERY is
 * that layout's media query on .admin-widget-grid in app.css.
 */
const STACKED_STREAM_LIMIT = 4;
const FIXED_ROWS_QUERY = "(min-width: 64rem) and (min-width: 1024px)";

export function NowPlayingWidget() {
  const t = useT();
  const sessionsQuery = useAdminSessions();
  const sessions = sessionsQuery.data ?? [];
  const fixedRows = useMediaQuery(FIXED_ROWS_QUERY, true);
  const rendered = sessions.slice(0, fixedRows ? MAX_RENDERED_STREAMS : STACKED_STREAM_LIMIT);
  const { setViewport, setGrid, observeCard, visibleCount } = useFittedCardCount(rendered.length);

  // Only a successful, genuinely empty load earns the strip. A skeleton or an
  // error keeps its full height: shrinking the widget to announce a failure
  // would hide the failure.
  const isIdle = !sessionsQuery.isLoading && !sessionsQuery.error && sessions.length === 0;
  useReportCollapsed(isIdle);

  if (isIdle) {
    return (
      <div className="flex h-full min-w-0 items-center gap-3">
        <div className="shrink-0 text-base font-bold">
          {t("web.admin.dashboard.widgets.nowPlaying.title")}
        </div>
        <div className="text-muted-foreground min-w-0 flex-1 truncate text-sm">
          {t("web.admin.dashboard.widgets.nowPlaying.idle")}
        </div>
        <Link
          to="/admin/activity"
          className="text-muted-foreground hover:text-primary shrink-0 text-[0.6875rem] transition-colors"
        >
          {t("web.admin.dashboard.widgets.nowPlaying.viewActivity")}
        </Link>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-3 flex shrink-0 items-center justify-between">
        <div className="text-base font-bold">
          {t("web.admin.dashboard.widgets.nowPlaying.title")}
        </div>
        {sessions.length > 0 && (
          <Link
            to="/admin/activity"
            className="text-muted-foreground hover:text-primary text-[0.6875rem] transition-colors"
          >
            {t("web.admin.dashboard.widgets.nowPlaying.viewAll", {
              count: sessions.length,
              count_text: sessions.length,
            })}
          </Link>
        )}
      </div>
      {/* The widget shows as many whole cards as its height allows instead of
          half-drawing one at the edge; the header's "View all" link counts
          every stream.
          Cards pair up only when the widget itself is wide enough for two, so a
          half-width widget shows one full-width card per row. The viewport
          stays positioned so it is the cards' offsetParent: the fit compares
          their offsetTop with its height. */}
      <div ref={setViewport} className="@container relative min-h-0 flex-1 overflow-hidden">
        {sessionsQuery.isLoading ? (
          <div className="grid grid-cols-1 gap-3.5 @2xl:grid-cols-2">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-[120px] rounded-2xl" />
            ))}
          </div>
        ) : sessionsQuery.error ? (
          <SectionError message={t("web.admin.dashboard.widgets.nowPlaying.loadFailed")} />
        ) : (
          /* The empty case never reaches here — it returned the collapsed strip
             above — so this branch always has at least one stream to show. */
          <>
            {/* Grid items stretch to their row, so both cards in a row match
                height. Rows stay sized to their own cards: auto-rows-fr would
                size every row to the tallest card, hidden ones included, and
                one wrapped hidden card would push rows out of the fit. */}
            <div ref={setGrid} className="grid grid-cols-1 gap-3.5 @2xl:grid-cols-2">
              {rendered.map((session, index) => {
                // Cards past the fit stay laid out, so a taller widget can
                // measure them, but are hidden from sight, focus, and readers.
                const hidden = index >= visibleCount;
                return (
                  <div
                    key={session.session_id}
                    ref={observeCard}
                    className={cn("flex", hidden && "invisible")}
                    inert={hidden}
                  >
                    <StreamCard session={session} showPoster={!hidden} />
                  </div>
                );
              })}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

/**
 * How many grid cards fit whole inside the viewport.
 *
 * Card height depends on how its tags wrap and the column count on the
 * widget's width, so the fit is measured from the laid-out cards rather than
 * derived. Every card stays laid out (hidden ones are only invisible), so the
 * count can grow again when the widget is resized taller.
 *
 * Rows size to their own cards, so a refresh can move a tall card between rows
 * without changing the grid's height; the observer watches every card as well
 * as the grid to catch that. The one change it still misses, a card joining a
 * half-full last row, is covered by storing "everything fits" as Infinity
 * rather than a count that would go stale.
 */
function useFittedCardCount(cardCount: number) {
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const gridRef = useRef<HTMLDivElement | null>(null);
  const [visibleCount, setVisibleCount] = useState(Number.POSITIVE_INFINITY);

  const measure = useCallback(() => {
    const viewport = viewportRef.current;
    const grid = gridRef.current;
    if (!viewport || !grid) return;
    const cards = Array.from(grid.children) as HTMLElement[];
    const height = viewport.clientHeight;
    const bottomOf = (card: HTMLElement) => card.offsetTop + card.offsetHeight;

    let next = Number.POSITIVE_INFINITY;
    const last = cards[cards.length - 1];
    if (last && bottomOf(last) > height) {
      // Keep one card even in a widget too short for it: an empty widget reads
      // as an error, and the card clips cleanly at the edge.
      next = Math.max(
        1,
        cards.findIndex((card) => bottomOf(card) > height),
      );
    }
    setVisibleCount(next);
  }, []);

  const observerRef = useRef<ResizeObserver | null>(null);
  const observe = useCallback(() => {
    observerRef.current?.disconnect();
    observerRef.current = null;
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    // The viewport tracks widget resizes; the grid and its cards track cards
    // growing, shrinking, or trading rows as their content changes.
    if (viewportRef.current) observer.observe(viewportRef.current);
    if (gridRef.current) {
      observer.observe(gridRef.current);
      for (const card of gridRef.current.children) observer.observe(card);
    }
    observerRef.current = observer;
  }, [measure]);

  // Cards mounted before the observer exists are picked up by observe() above;
  // this covers cards that arrive with a later refresh. Always return the
  // cleanup: a callback ref that returns none is called again with null on
  // unmount.
  const observeCard = useCallback((node: HTMLDivElement) => {
    observerRef.current?.observe(node);
    return () => observerRef.current?.unobserve(node);
  }, []);

  // Callback refs: the grid mounts only after loading finishes, so an effect
  // keyed on nothing would never see it.
  const setViewport = useCallback(
    (node: HTMLDivElement | null) => {
      viewportRef.current = node;
      observe();
    },
    [observe],
  );
  const setGrid = useCallback(
    (node: HTMLDivElement | null) => {
      gridRef.current = node;
      observe();
    },
    [observe],
  );

  return { setViewport, setGrid, observeCard, visibleCount: Math.min(visibleCount, cardCount) };
}

// A hidden card skips its poster request (the placeholder icon stands in); the
// poster box has a fixed aspect ratio, so the card keeps the height the fit
// measures either way.
function StreamCard({ session, showPoster }: { session: AdminSession; showPoster: boolean }) {
  const t = useT();
  const isEpisode = isEpisodeSession(session);
  // The show leads for an episode; the episode line follows it.
  const title = isEpisode
    ? session.series_title
    : session.media_title ||
      t("web.admin.dashboard.widgets.nowPlaying.fileFallback", {
        id: session.media_file_id,
      });
  const subtitle = getPlaybackSessionSubtitle(session);
  const username =
    session.username ||
    t("web.admin.dashboard.widgets.nowPlaying.userFallback", { id: session.user_id });
  const elapsed = formatRelativeTime(session.started_at, {
    rounding: "floor",
    justNowLabel: t("web.admin.dashboard.widgets.nowPlaying.justNow"),
  });
  const position = formatPlaybackPosition(session);
  const clientLabel = getSessionClientLabel(session);
  const method = classifyActivityMethod(session);
  const methodMeta = activityMethodMeta(method);

  return (
    <div className="surface-panel flex min-w-0 flex-1 gap-3.5 rounded-2xl border-0 p-3.5 transition-colors duration-150">
      {/* Poster */}
      <div
        className="bg-surface border-border relative flex w-[60px] flex-shrink-0 items-center justify-center self-start overflow-hidden rounded-lg border"
        style={{ aspectRatio: "2/3" }}
      >
        {showPoster && session.poster_url ? (
          <img
            src={session.poster_url}
            alt={session.media_title}
            className={`h-full w-full object-cover transition-opacity ${session.is_paused ? "opacity-45" : ""}`}
          />
        ) : (
          <Play className={`text-primary/40 h-5 w-5 ${session.is_paused ? "opacity-45" : ""}`} />
        )}
        {/* Icon only: the poster is 58px inside, narrower than the word in
            most languages, so the word is left to screen readers. */}
        {session.is_paused ? (
          <div className="absolute inset-0 flex items-center justify-center bg-black/35">
            <div className="border-border/40 bg-background/90 text-foreground flex h-6 w-6 items-center justify-center rounded-full border shadow-sm backdrop-blur">
              <Pause className="h-3 w-3 fill-current" aria-hidden="true" />
              <span className="sr-only">{t("web.admin.dashboard.widgets.nowPlaying.paused")}</span>
            </div>
          </div>
        ) : null}
      </div>

      {/* Info */}
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="mb-1.5 flex min-w-0 flex-col">
          {session.media_item_id ? (
            <Link
              to={`/item/${session.media_item_id}`}
              dir="auto"
              className="bidi-text hover:text-primary truncate text-sm font-bold transition-colors hover:underline"
            >
              {title}
            </Link>
          ) : (
            <div dir="auto" className="bidi-text truncate text-sm font-bold">
              {title}
            </div>
          )}
          {subtitle ? (
            <div className="text-muted-foreground truncate text-xs">{subtitle}</div>
          ) : null}
        </div>

        {/* Tags. Capped at one line so every card has the same height and
            the widget's rows stay even: tags that would wrap are clipped
            whole. The cap is the pills' height: the 0.75rem line height
            each pill is given here (overriding its own), their 0.25rem of
            padding, and 2px of border. Keep it in rem so it grows with
            Text Size. */}
        <div className="mb-1.5 flex max-h-[calc(1rem+2px)] flex-wrap gap-1 overflow-hidden leading-3 [&>*]:leading-3">
          <span
            className={`inline-flex rounded border px-1.5 py-0.5 text-[0.5625rem] font-semibold ${methodMeta.badgeClass}`}
          >
            {methodMeta.label}
          </span>
          <JellyfinSessionPill session={session} />
          {clientLabel ? (
            <span
              title={session.client_user_agent || clientLabel}
              className="border-border/60 bg-muted/30 text-muted-foreground inline-flex max-w-[9rem] rounded border px-1.5 py-0.5 text-[0.5625rem] font-semibold"
            >
              <span dir="auto" className="truncate">
                {clientLabel}
              </span>
            </span>
          ) : null}
          <PlaybackRouteBadges session={session} />
          {(session.profile_name || session.profile_id) && (
            <SessionProfilePill label={session.profile_name || session.profile_id} />
          )}
        </div>

        {/* User. One line at every width, so the card never grows taller:
            the name truncates and the times keep their full width. */}
        <div className="mt-auto flex items-center gap-1.5">
          <div
            className="text-primary-foreground flex h-5.5 w-5.5 shrink-0 items-center justify-center rounded-full text-[0.5625rem] font-bold"
            style={{ background: `var(--primary)` }}
          >
            {getInitial(username)}
          </div>
          <span dir="auto" className="min-w-0 truncate text-xs font-medium">
            {username}
          </span>
          <span className="text-muted-foreground ms-auto shrink-0 text-[0.625rem] whitespace-nowrap">
            <span className="tabular-nums">{position}</span>
            {/* eslint-disable-next-line formatjs/no-literal-string-in-jsx -- punctuation separating two already-formatted times */}
            <span> · {elapsed}</span>
          </span>
        </div>
      </div>
    </div>
  );
}
