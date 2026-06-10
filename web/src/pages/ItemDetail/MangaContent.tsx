import { useMemo } from "react";
import { BookOpen } from "lucide-react";
import { Link } from "react-router";
import type { ItemDetail, MangaChapter } from "@/api/types";
import PageBack from "@/components/PageBack";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { buildMediaPlayHref } from "@/lib/mediaNavigation";
import { groupMangaChapters, prettifyVolumeLabel } from "@/lib/mangaChapters";
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

export default function MangaContent({
  item,
  libraryId,
}: {
  item: ItemDetail & { type: "manga" };
  libraryId?: number;
}) {
  useAmbientColor(item.poster_thumbhash);
  const groups = useMemo(
    () => groupMangaChapters(item.manga?.chapters ?? []),
    [item.manga?.chapters],
  );
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

      <div className="page-shell space-y-10 py-10">
        {groups.length === 0 ? (
          <p className="text-muted-foreground text-sm">No chapters found.</p>
        ) : (
          groups.map((group) => (
            <section key={group.volume ?? "__loose__"} className="space-y-3">
              <h2 className="text-foreground text-lg font-bold tracking-tight">
                {group.volume === null ? "Chapters" : prettifyVolumeLabel(group.volume)}
              </h2>
              <ul className="divide-border/40 border-border/40 divide-y overflow-hidden rounded-lg border">
                {group.chapters.map((chapter) => (
                  <li key={chapter.content_id}>
                    <Link
                      to={buildMediaPlayHref({
                        contentId: chapter.content_id,
                        type: "ebook",
                        libraryId,
                      })}
                      className="hover:bg-muted/40 flex items-center gap-3 px-4 py-3 transition-colors"
                    >
                      <BookOpen className="text-muted-foreground size-[18px] flex-shrink-0" />
                      <span className="text-foreground/90 truncate text-[15px] font-medium">
                        {chapterLabel(chapter)}
                      </span>
                      {chapter.title && chapterLabel(chapter) !== chapter.title && (
                        <span className="text-muted-foreground truncate text-sm">
                          {chapter.title}
                        </span>
                      )}
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          ))
        )}
      </div>
    </div>
  );
}
