import { useMemo, useRef, type MouseEvent } from "react";
import { joinList } from "@/lib/appLocale";
import { Link } from "react-router";
import { Info, Pause, Play } from "lucide-react";
import type { ResolvedSection } from "@/api/types";
import MediaCardArtwork from "@/components/MediaCardArtwork";
import MediaItemMenu from "@/components/MediaItemMenu";
import { homeDismissalForCard } from "@/hooks/queries/homeDismissals";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useT } from "@/i18n";

import { useAmbientColor } from "@/hooks/useAmbientColor";
import { buildChapterList, findChapterAt, totalAudiobookDuration } from "@/lib/audiobooks/chapters";
import { formatHoursMinutes, formatListeningTimeLeft } from "@/lib/audiobooks/duration";
import { audiobookFilesFromVersions } from "@/lib/audiobooks/files";
import { buildItemHref, buildMediaPlayHref } from "@/lib/mediaNavigation";
import { buildSectionCatalogHref } from "@/pages/catalogSearchParams";
import { useSectionTitle } from "@/lib/sectionTitle";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";

import "@/pages/audiobooks/audiobooks.css";

interface NowListeningHeroProps {
  /** A resolved continue-listening section; items are in-progress audiobooks. */
  section: ResolvedSection;
  libraryId?: number;
}

/** In-progress books listed beside the featured one before "Explore all". */
const ALSO_LISTENING_LIMIT = 4;

function namesFromPeople(people: Array<{ name?: string }> | undefined): string | undefined {
  const names = joinList(
    (people ?? []).map((person) => person.name?.trim()).filter((name): name is string => !!name),
  );
  return names || undefined;
}

function percentOf(position: number | undefined, duration: number | undefined): number {
  if (!position || !duration || duration <= 0) return 0;
  return Math.min(100, Math.max(0, (position / duration) * 100));
}

type SectionItem = ResolvedSection["items"][number];

/**
 * One of the other books in progress beside the hero: the book's link, a play
 * control that resumes it, and the card menu with Remove from Continue
 * Listening, as the Continue Listening card it replaces had.
 */
function AlsoListeningRow({ item, libraryId }: { item: SectionItem; libraryId?: number }) {
  const t = useT();
  const rowRef = useRef<HTMLDivElement>(null);
  const left = formatListeningTimeLeft(item.position_seconds, item.duration_seconds);
  return (
    <div
      ref={rowRef}
      className="group/card media-card-longpress relative grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-1 rounded-xl p-1.5 transition-colors hover:bg-white/[0.06]"
    >
      <ViewTransitionLink
        to={buildItemHref({ contentId: item.content_id, libraryId })}
        className="grid min-w-0 grid-cols-[3.25rem_minmax(0,1fr)] items-center gap-3 rounded-lg"
      >
        <span className="block overflow-hidden rounded-md">
          <MediaCardArtwork
            src={item.poster_url}
            alt=""
            mediaType="audiobook"
            thumbhash={item.poster_thumbhash}
            square
            scrim="none"
            lazy
          />
        </span>
        <span className="grid min-w-0 gap-1">
          <span className="truncate text-sm font-semibold">{item.title}</span>
          {left && <span className="text-muted-foreground text-xs">{left}</span>}
          <span className="h-[3px] w-full overflow-hidden rounded-full bg-white/12">
            <span
              className="bg-primary block h-full rounded-full"
              style={{ width: `${percentOf(item.position_seconds, item.duration_seconds)}%` }}
            />
          </span>
        </span>
      </ViewTransitionLink>
      <ViewTransitionLink
        to={buildMediaPlayHref({ contentId: item.content_id, type: item.type, libraryId })}
        aria-label={t("web.home.continueWatching.playTitle", { title: item.title })}
        className="text-foreground grid size-9 place-items-center rounded-full transition-colors hover:bg-white/10"
      >
        {/* eslint-disable-next-line silo/no-physical-direction -- centres the play triangle, which doesn't mirror */}
        <Play className="ml-0.5 h-4 w-4 fill-current" />
      </ViewTransitionLink>
      <div className="relative size-9">
        <MediaItemMenu
          contentId={item.content_id}
          mediaType={item.type}
          libraryId={libraryId}
          userState={item.user_state}
          showFavoriteShortcut={false}
          dismissAction={homeDismissalForCard(item)}
          hasPartialProgress
          hasResumePosition={(item.position_seconds ?? 0) > 0}
          longPressRef={rowRef}
          itemTitle={item.title}
        />
      </div>
    </div>
  );
}

/**
 * The audiobook library "hero": the most recent in-progress book as a resume
 * deck (cover, chapter position, time left, Resume), tinted with the cover's
 * colour, with the other in-progress books listed beside it.
 */
export default function NowListeningHero({ section, libraryId }: NowListeningHeroProps) {
  const t = useT();
  const deck = section.items[0];
  const rest = section.items.slice(1);
  const sectionTitle = useSectionTitle();
  const audiobookPlayback = useAudiobookPlaybackController();
  // The section payload has no chapters or credits; the item detail fills in
  // author/narrator, chapter marks, and the files needed for one-click resume.
  const { data: fetchedDetail } = useCatalogItemDetail(deck?.content_id, libraryId);
  useAmbientColor(deck?.poster_thumbhash);

  // The detail query keeps previous data while a new deck item loads, so a
  // Resume click in that window could start the new book with the previous
  // book's files. Only trust a detail payload that matches the deck item.
  const detail =
    fetchedDetail?.type === "audiobook" && fetchedDetail.content_id === deck?.content_id
      ? fetchedDetail
      : undefined;

  const files = useMemo(
    () => (detail ? audiobookFilesFromVersions(detail.versions) : []),
    [detail],
  );
  // Built on each render so the fallback "Chapter N" labels it shows follow
  // the interface language.
  const chapters = buildChapterList(files);

  if (!deck) return null;

  const detailProgress =
    detail?.user_data && "position_seconds" in detail.user_data ? detail.user_data : undefined;
  const isActive = audiobookPlayback?.active?.contentId === deck.content_id;
  const isActivePlaying = isActive && Boolean(audiobookPlayback?.active?.playing);
  const livePosition = isActive ? (audiobookPlayback?.active?.currentTime ?? null) : null;
  const positionSeconds =
    livePosition ?? detailProgress?.position_seconds ?? deck.position_seconds ?? 0;
  const durationSeconds =
    detail?.audiobook?.total_duration_seconds ||
    totalAudiobookDuration(files) ||
    deck.duration_seconds ||
    0;

  const author = namesFromPeople(detail?.audiobook?.authors);
  const narrator = namesFromPeople(detail?.audiobook?.narrators);
  const chapter = chapters.length > 0 ? findChapterAt(chapters, positionSeconds) : null;
  const progressPercent = percentOf(positionSeconds, durationSeconds);
  const timeLeft = formatListeningTimeLeft(positionSeconds, durationSeconds);
  // Whether the chapter has a title of its own is decided on the English
  // data, as before, not by comparing translated text (D44).
  let chapterTitle: string | undefined;
  if (chapter) {
    const entry = chapters[chapter.index - 1];
    const englishLabel = entry && (entry.chapter.title || `Chapter ${entry.chapter.index + 1}`);
    if (englishLabel && englishLabel !== `Chapter ${chapter.index}`) {
      chapterTitle = chapter.label;
    }
  }

  const playHref = buildMediaPlayHref({
    contentId: deck.content_id,
    type: deck.type,
    libraryId,
  });
  const itemHref = buildItemHref({ contentId: deck.content_id, libraryId });

  const handleResumeClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (isActive) {
      event.preventDefault();
      audiobookPlayback?.toggleActivePlayback();
      return;
    }
    if (files.length > 0 && audiobookPlayback) {
      event.preventDefault();
      audiobookPlayback.startPlayback({
        contentId: deck.content_id,
        title: deck.title,
        author,
        narrator,
        posterUrl: deck.poster_url,
        posterThumbhash: deck.poster_thumbhash,
        files,
        initialPositionSeconds: positionSeconds > 0 ? positionSeconds : 0,
      });
    }
    // Without files yet (detail still loading), fall through to the detail
    // page link, which auto-starts playback via ?play=1.
  };

  const resumeLabel = isActivePlaying
    ? t("web.audiobooks.hero.pause")
    : positionSeconds > 0
      ? t("web.audiobooks.hero.resume")
      : t("web.audiobooks.hero.listen");

  const alsoListening = rest.slice(0, ALSO_LISTENING_LIMIT);
  const shownTitle = sectionTitle(section);
  // The server can truncate the section (has_more), so a short payload can
  // still hold more books than the panel shows.
  const exploreHref =
    rest.length > alsoListening.length || section.has_more
      ? libraryId
        ? buildSectionCatalogHref({
            scope: "library",
            libraryId,
            sectionId: section.id,
            title: shownTitle,
          })
        : buildSectionCatalogHref({ scope: "home", sectionId: section.id, title: shownTitle })
      : undefined;

  return (
    <section
      className="relative isolate -mt-[96px] w-full sm:-mt-[104px]"
      aria-label={t("web.audiobooks.hero.ariaLabel")}
    >
      <div aria-hidden="true" className="audiobook-tint" style={{ height: "100%" }} />

      <div className="relative px-4 pt-28 pb-10 sm:px-6 sm:pt-32 sm:pb-12 lg:px-10 xl:px-12">
        <div className="grid max-w-[1380px] grid-cols-[minmax(0,1fr)] items-center gap-6 sm:grid-cols-[12rem_minmax(0,1fr)] sm:gap-8 xl:grid-cols-[14rem_minmax(0,1fr)_19rem] xl:gap-10">
          <ViewTransitionLink
            to={itemHref}
            className="block w-36 overflow-hidden rounded-2xl shadow-[0_26px_50px_-20px_rgb(0_0_0/0.85)] sm:w-48 xl:w-56"
          >
            <MediaCardArtwork
              src={deck.poster_url}
              alt={deck.title}
              mediaType="audiobook"
              thumbhash={deck.poster_thumbhash}
              square
              scrim="none"
              priority
            />
          </ViewTransitionLink>

          <div className="min-w-0">
            {chapter && (
              <p className="text-foreground/75 flex flex-wrap items-baseline gap-x-2.5 gap-y-1 text-sm">
                <span>
                  {t("web.audiobooks.hero.chapterPosition", {
                    index: chapter.index,
                    total: chapters.length,
                  })}
                </span>
                {chapterTitle && (
                  <span className="text-foreground font-semibold">{chapterTitle}</span>
                )}
              </p>
            )}
            <h1
              className="font-display mt-2 max-w-3xl text-3xl font-extrabold tracking-[-0.03em] text-balance sm:text-4xl lg:text-[2.75rem] lg:leading-[1.06]"
              style={{ textShadow: "var(--hero-text-shadow, none)" }}
            >
              {deck.title}
            </h1>
            {(author || narrator) && (
              <p className="text-foreground/75 mt-2 max-w-2xl truncate text-sm sm:text-base">
                {author && narrator
                  ? t("web.audiobooks.hero.authorAndNarrator", { author, narrator })
                  : narrator
                    ? t("web.audiobooks.hero.narratedBy", { narrator })
                    : author}
              </p>
            )}

            {durationSeconds > 0 && (
              <div className="mt-6 max-w-xl">
                <div className="h-1.5 w-full overflow-hidden rounded-full bg-white/15">
                  <div
                    className="bg-primary h-full rounded-full transition-[width] duration-(--duration-normal)"
                    style={{ width: `${progressPercent}%` }}
                  />
                </div>
                <div className="mt-2 flex items-baseline justify-between gap-3 text-xs sm:text-sm">
                  <span className="text-foreground/70 truncate">
                    {positionSeconds > 0
                      ? t("web.detail.audiobook.listenedProgress", {
                          time: formatHoursMinutes(positionSeconds),
                          percent: Math.round(progressPercent),
                        })
                      : formatHoursMinutes(durationSeconds)}
                  </span>
                  {timeLeft && positionSeconds > 0 && (
                    <span className="text-foreground shrink-0 font-semibold">{timeLeft}</span>
                  )}
                </div>
              </div>
            )}

            <div className="mt-6 flex flex-wrap items-center gap-3">
              <Link
                to={playHref}
                onClick={handleResumeClick}
                className="pill pill-primary transition-colors duration-(--duration-fast)"
              >
                {isActivePlaying ? (
                  <Pause className="h-4 w-4 fill-current" />
                ) : (
                  <Play className="h-4 w-4 fill-current" />
                )}
                {resumeLabel}
              </Link>
              <ViewTransitionLink
                to={itemHref}
                className="pill pill-glass transition-colors duration-(--duration-fast)"
              >
                <Info className="h-4 w-4" />
                {t("web.audiobooks.hero.moreInfo")}
              </ViewTransitionLink>
            </div>
          </div>

          {alsoListening.length > 0 && (
            <aside className="bg-background/30 grid gap-1 self-center rounded-2xl border border-white/[0.07] p-3 backdrop-blur-md sm:col-span-2 sm:grid-cols-2 xl:col-span-1 xl:grid-cols-1 xl:content-start">
              <div className="flex items-baseline justify-between gap-3 px-2 pt-1 pb-2 sm:col-span-2 xl:col-span-1">
                <h3 className="text-sm font-bold">{t("web.audiobooks.hero.alsoListening")}</h3>
                {exploreHref && (
                  <Link
                    to={exploreHref}
                    className="text-muted-foreground hover:text-foreground text-xs font-semibold transition-colors"
                  >
                    {t("web.catalog.carousel.exploreAll")}
                  </Link>
                )}
              </div>
              {alsoListening.map((item) => (
                <AlsoListeningRow key={item.content_id} item={item} libraryId={libraryId} />
              ))}
            </aside>
          )}
        </div>
      </div>
    </section>
  );
}
