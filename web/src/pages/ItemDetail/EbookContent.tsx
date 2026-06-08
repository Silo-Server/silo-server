import { useState } from "react";
import { Download } from "lucide-react";
import type { ItemDetail } from "@/api/types";
import DownloadVersionPicker from "@/components/DownloadVersionPicker";
import MediaLocations from "@/components/MediaLocations";
import PageBack from "@/components/PageBack";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/useAuth";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { RelatedRail } from "@/pages/audiobooks/components/RelatedRail";
import DetailHero from "./DetailHero";
import MetadataBadges from "./components/MetadataBadges";
import ScoreRow from "./components/ScoreRow";

function authorNames(item: ItemDetail): string[] {
  const extensionAuthors = (item.ebook?.authors ?? [])
    .map((author) => author.name)
    .filter((name) => name.trim() !== "");
  if (extensionAuthors.length > 0) {
    return extensionAuthors;
  }
  return (item.crew ?? [])
    .filter((credit) => credit.job === "Author")
    .map((credit) => credit.name)
    .filter((name) => name.trim() !== "");
}

export default function EbookContent({ item }: { item: ItemDetail & { type: "ebook" } }) {
  useAmbientColor(item.poster_thumbhash);
  const { user } = useAuth();
  const [downloadOpen, setDownloadOpen] = useState(false);
  const authors = authorNames(item);
  const publisher = item.ebook?.publisher || (item.studios ?? [])[0];
  const year = item.year ? String(item.year) : "";
  const canDownload = Boolean(user?.download_allowed && item.versions.length > 0);

  return (
    <div>
      <DetailHero
        title={item.title}
        topNav={<PageBack />}
        context="Ebook"
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
        crewLine={
          authors.length > 0 ? (
            <div className="text-muted-foreground text-[13px]">
              <span className="text-muted-foreground/60">By </span>
              <span className="text-foreground/70 font-medium">{authors.join(", ")}</span>
            </div>
          ) : undefined
        }
        genres={item.genres}
        actions={
          canDownload ? (
            <Button
              type="button"
              className="h-11 gap-2.5 rounded-full px-6 text-[15px] font-bold tracking-wide shadow-md"
              onClick={() => setDownloadOpen(true)}
            >
              <Download className="size-[18px]" />
              Download
            </Button>
          ) : undefined
        }
      />

      <div className="page-shell space-y-12 py-10 sm:space-y-14">
        {item.ebook?.series && item.ebook.series.entries.length > 0 && (
          <RelatedRail
            heading={item.ebook.series.name ? `In ${item.ebook.series.name}` : "In this series"}
            items={item.ebook.series.entries.map((entry) => ({
              content_id: entry.content_id,
              title: entry.title,
              poster_url: entry.poster_url,
              subtitle:
                typeof entry.series_index === "number" ? `Book ${entry.series_index}` : undefined,
              highlight: entry.content_id === item.content_id,
            }))}
          />
        )}

        {(item.ebook?.related.also_by_author ?? []).length > 0 && (
          <RelatedRail
            heading={`Also by ${authors[0] ?? "this author"}`}
            items={(item.ebook?.related.also_by_author ?? []).map((entry) => ({
              content_id: entry.content_id,
              title: entry.title,
              poster_url: entry.poster_url,
              subtitle: entry.year ? String(entry.year) : undefined,
            }))}
          />
        )}

        {(item.ebook?.related.similar ?? []).length > 0 && (
          <RelatedRail
            heading="You might also like"
            items={(item.ebook?.related.similar ?? []).map((entry) => ({
              content_id: entry.content_id,
              title: entry.title,
              poster_url: entry.poster_url,
              subtitle: entry.year ? String(entry.year) : undefined,
            }))}
          />
        )}

        <MediaLocations
          title="Files"
          versions={item.versions}
          emptyMessage="No ebook files found."
        />
      </div>

      <DownloadVersionPicker
        open={downloadOpen}
        onOpenChange={setDownloadOpen}
        versions={item.versions}
        title={item.title}
      />
    </div>
  );
}
