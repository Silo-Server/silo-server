import { useMemo } from "react";
import { BookOpen } from "lucide-react";
import { Link } from "react-router";
import type { ItemDetail, MangaChapter } from "@/api/types";
import PageBack from "@/components/PageBack";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { buildMediaPlayHref } from "@/lib/mediaNavigation";
import { buildMangaList } from "@/lib/mangaChapters";
import DetailHero from "./DetailHero";
import MetadataBadges from "./components/MetadataBadges";
import ScoreRow from "./components/ScoreRow";

function genreHref(genre: string, libraryId?: number): string {
  const params = new URLSearchParams();
  if (libraryId) {
    params.set("tab", "library");
    params.set("genre", genre);
    return `/library/${libraryId}?${params.toString()}`;
  }
  params.set("source", "query");
  params.set("type", "manga");
  params.set("genre", genre);
  return `/catalog?${params.toString()}`;
}

// chapterLabel prefers a "Chapter <n>" form derived from the index, falling
// back to the chapter's own title when no index is available.
function chapterLabel(chapter: MangaChapter): string {
  if (typeof chapter.chapter_index === "number") {
    return `Chapter ${chapter.chapter_index}`;
  }
  return chapter.title || "Chapter";
}

// MangaRow is a single flat clickable reader row used for volume units, loose
// chapters, and chapters nested inside a volume section.
function MangaRow({
  chapter,
  label,
  libraryId,
}: {
  chapter: MangaChapter;
  label: string;
  libraryId?: number;
}) {
  return (
    <Link
      to={buildMediaPlayHref({ contentId: chapter.content_id, type: "ebook", libraryId })}
      className="hover:bg-muted/40 flex items-center gap-3 px-4 py-3 transition-colors"
    >
      <BookOpen className="text-muted-foreground size-[18px] flex-shrink-0" />
      <span className="text-foreground/90 truncate text-[15px] font-medium">{label}</span>
    </Link>
  );
}

export default function MangaContent({
  item,
  libraryId,
}: {
  item: ItemDetail & { type: "manga" };
  libraryId?: number;
}) {
  useAmbientColor(item.poster_thumbhash);
  const entries = useMemo(() => buildMangaList(item.manga?.chapters ?? []), [item.manga?.chapters]);
  const year = item.year ? String(item.year) : "";
  const publisher = item.studios?.[0];

  return (
    <div>
      <DetailHero
        title={item.title}
        topNav={<PageBack />}
        context="Manga"
        studioLabel={publisher}
        backdropUrl={item.backdrop_url}
        backdropThumbhash={item.backdrop_thumbhash}
        posterUrl={item.poster_url}
        posterThumbhash={item.poster_thumbhash}
        metadata={
          <MetadataBadges
            year={year || undefined}
            contentRating={item.content_rating || undefined}
          />
        }
        scoreRow={
          <ScoreRow
            ratingImdb={item.rating_imdb}
            ratingRtCritic={item.rating_rt_critic}
            ratingRtAudience={item.rating_rt_audience}
          />
        }
        overview={item.overview}
        genres={item.genres}
        genreHref={(genre) => genreHref(genre, libraryId)}
      />

      <div className="page-shell space-y-6 py-10">
        {entries.length === 0 ? (
          <p className="text-muted-foreground text-sm">No chapters found.</p>
        ) : (
          <ul className="divide-border/40 border-border/40 divide-y overflow-hidden rounded-lg border">
            {entries.map((entry) =>
              entry.kind === "section" ? (
                <li key={`section-${entry.label}`}>
                  <h2 className="text-muted-foreground bg-muted/30 px-4 py-2 text-sm font-bold tracking-tight uppercase">
                    {entry.label}
                  </h2>
                  <ul className="divide-border/40 divide-y">
                    {entry.chapters.map((chapter) => (
                      <li key={chapter.content_id} className="pl-4">
                        <MangaRow
                          chapter={chapter}
                          label={chapterLabel(chapter)}
                          libraryId={libraryId}
                        />
                      </li>
                    ))}
                  </ul>
                </li>
              ) : (
                <li key={entry.chapter.content_id}>
                  <MangaRow chapter={entry.chapter} label={entry.label} libraryId={libraryId} />
                </li>
              ),
            )}
          </ul>
        )}
      </div>
    </div>
  );
}
