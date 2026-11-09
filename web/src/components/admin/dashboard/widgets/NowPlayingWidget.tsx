import { useCallback, useRef, useState } from "react";
import { Link } from "react-router";
import { Pause, Play } from "lucide-react";
import { JellyfinSessionPill } from "@/components/JellyfinSessionPill";
import { PlaybackRouteBadges } from "@/components/PlaybackRouteBadges";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import {
  activityMethodMeta,
  classifyActivityMethod,
  formatPlaybackPosition,
  getSessionClientLabel,
} from "@/pages/adminActivityPresentation";
import type { AdminSession } from "@/api/types";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import { SectionError } from "../feedback";
import { useReportCollapsed } from "../widgetChrome";
import { SessionProfilePill } from "./SessionProfilePill";

/**
 * Most cards the widget mounts at once. The widget shows only what fits, and a
 * busy server can hold hundreds of sessions; the activity page lists the rest.
 */
const MAX_RENDERED_STREAMS = 40;

/**
 * Cards shown below lg, where dashboard rows size to their content and there is
 * no widget height to fit against (matches the lg rule in app.css).
 */
const STACKED_STREAM_LIMIT = 4;
const FIXED_ROWS_QUERY = "(min-width: 1024px)";

/** Height kept free under the last visible row for the "+N more" link. */
const MORE_LINK_HEIGHT_PX = 24;

export function NowPlayingWidget() {
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
        <div className="shrink-0 text-base font-bold">Now Playing</div>
        <div className="text-muted-foreground min-w-0 flex-1 truncate text-sm">
          Nothing playing right now
        </div>
        <Link
          to="/admin/activity"
          className="text-muted-foreground hover:text-primary shrink-0 text-[11px] transition-colors"
        >
          View activity ›
        </Link>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-3 flex shrink-0 items-center justify-between">
        <div className="text-base font-bold">Now Playing</div>
        {sessions.length > 0 && (
          <Link
            to="/admin/activity"
            className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
          >
            View all {sessions.length} streams ›
          </Link>
        )}
      </div>
      {/* The widget shows as many whole cards as its height allows; the rest
          are counted in the "+N more" link instead of half-drawn at the edge. */}
      <div ref={setViewport} className="relative min-h-0 flex-1 overflow-hidden">
        {sessionsQuery.isLoading ? (
          <div className="grid grid-cols-1 gap-3.5 lg:grid-cols-2">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-[120px] rounded-2xl" />
            ))}
          </div>
        ) : sessionsQuery.error ? (
          <SectionError message="Failed to load streams." />
        ) : (
          /* The empty case never reaches here — it returned the collapsed strip
             above — so this branch always has at least one stream to show. */
          <>
            {/* Grid items stretch to their row, so both cards in a row match
                height. Rows stay sized to their own cards: auto-rows-fr would
                size every row to the tallest card, hidden ones included, and
                one wrapped hidden card would push rows out of the fit. */}
            <div ref={setGrid} className="grid grid-cols-1 gap-3.5 lg:grid-cols-2">
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
            {sessions.length > visibleCount && (
              <Link
                to="/admin/activity"
                className={cn(
                  "text-muted-foreground hover:text-primary block text-center text-[12px] leading-6 transition-colors",
                  // In a fixed-height widget the link sits in the strip the
                  // fit leaves free; a content-sized one just flows after.
                  fixedRows ? "absolute inset-x-0 bottom-0" : "mt-1",
                )}
              >
                +{sessions.length - visibleCount} more active streams
              </Link>
            )}
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
 * breakpoint, so the fit is measured from the laid-out cards rather than
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
      // Not everything fits, so the "+N more" link needs its strip too.
      const limit = height - MORE_LINK_HEIGHT_PX;
      // Keep one card even in a widget too short for it: a lone link reads as
      // an error, and the card clips cleanly at the edge.
      next = Math.max(
        1,
        cards.findIndex((card) => bottomOf(card) > limit),
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
  const isEpisode =
    session.series_name && session.season_number != null && session.episode_number != null;
  const title = isEpisode
    ? session.episode_name || `S${session.season_number}E${session.episode_number}`
    : session.media_title || `File #${session.media_file_id}`;
  const username = session.username || `User #${session.user_id}`;
  const startedAgo = formatRelativeTime(session.started_at, { rounding: "floor" });
  const position = formatPlaybackPosition(session);
  const duration = session.file_duration ?? 0;
  const clientLabel = getSessionClientLabel(session);
  const method = classifyActivityMethod(session);
  const methodMeta = activityMethodMeta(method);

  return (
    <div className="surface-panel flex flex-1 gap-3.5 rounded-2xl border-0 p-3.5 transition-colors duration-150">
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
        {session.is_paused ? (
          <div className="absolute inset-0 flex items-center justify-center bg-black/35">
            <div className="border-border/40 bg-background/90 text-foreground inline-flex items-center gap-1 rounded-full border px-2 py-1 text-[10px] font-semibold shadow-sm backdrop-blur">
              <Pause className="h-3 w-3" />
              Paused
            </div>
          </div>
        ) : null}
      </div>

      {/* Info */}
      <div className="flex min-w-0 flex-1 flex-col">
        {isEpisode ? (
          <>
            {session.content_id ? (
              <Link
                to={`/item/${session.content_id}`}
                className="hover:text-primary truncate text-sm font-bold transition-colors"
              >
                {title}
              </Link>
            ) : (
              <div className="truncate text-sm font-bold">{title}</div>
            )}
            <div className="text-muted-foreground mb-1.5 truncate text-xs">
              S{session.season_number} · E{session.episode_number}
              {session.series_name ? ` — ${session.series_name}` : ""}
            </div>
          </>
        ) : (
          <>
            {session.content_id ? (
              <Link
                to={`/item/${session.content_id}`}
                className="hover:text-primary truncate text-sm font-bold transition-colors"
              >
                {title}
              </Link>
            ) : (
              <div className="truncate text-sm font-bold">{title}</div>
            )}
            {session.media_type && (
              <div className="text-muted-foreground mb-1.5 truncate text-xs">
                {session.media_type === "movie" ? "Movie" : "Series"}
              </div>
            )}
          </>
        )}

        {/* Tags */}
        <div className="mb-1.5 flex flex-wrap gap-1">
          <span
            className={`inline-flex rounded border px-1.5 py-0.5 text-[9px] font-semibold ${methodMeta.badgeClass}`}
          >
            {methodMeta.label}
          </span>
          <JellyfinSessionPill session={session} />
          {clientLabel ? (
            <span
              title={session.client_user_agent || clientLabel}
              className="border-border/60 bg-muted/30 text-muted-foreground inline-flex max-w-[9rem] rounded border px-1.5 py-0.5 text-[9px] font-semibold"
            >
              <span className="truncate">{clientLabel}</span>
            </span>
          ) : null}
          <PlaybackRouteBadges session={session} />
          {(session.profile_name || session.profile_id) && (
            <SessionProfilePill label={session.profile_name || session.profile_id} />
          )}
        </div>

        <div className="mt-auto">
          {/* Position: the bar needs a duration; the time shows either way. */}
          <div className="mb-1.5 flex items-center gap-2">
            {duration > 0 ? (
              <Progress
                value={(session.position_seconds / duration) * 100}
                aria-label="Playback position"
                aria-valuetext={position}
                className={cn(
                  "bg-foreground/10 h-1 flex-1 rounded-full",
                  session.is_paused && "opacity-45",
                )}
              />
            ) : null}
            <span className="text-muted-foreground ml-auto shrink-0 text-[10px] tabular-nums">
              {position}
            </span>
          </div>

          {/* User */}
          <div className="flex items-center gap-1.5">
            <div
              className="text-primary-foreground flex h-[22px] w-[22px] shrink-0 items-center justify-center rounded-full text-[9px] font-bold"
              style={{ background: `var(--primary)` }}
            >
              {username.charAt(0).toUpperCase()}
            </div>
            <span className="min-w-0 truncate text-xs font-medium">{username}</span>
            {startedAgo ? (
              <span className="text-muted-foreground ml-auto shrink-0 text-[10px]">
                Started {startedAgo}
              </span>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  );
}
