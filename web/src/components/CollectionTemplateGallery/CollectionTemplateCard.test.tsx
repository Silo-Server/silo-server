import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { CollectionTemplateCard } from "./CollectionTemplateCard";
import type { CollectionTemplate } from "@/lib/collectionTemplates";

const baseTemplate: CollectionTemplate = {
  id: "tmdb_trending_movies_week",
  title: "Trending Movies This Week",
  description: "Top trending movies on TMDB over the past seven days.",
  icon: "🎬",
  category: "trending",
  source: "tmdb",
  media_kind: "movie",
  default_limit: 50,
  default_sync_schedule: "0 6 * * *",
  featured: true,
  tmdb: { preset: "trending", media_type: "movie", time_window: "week" },
};

describe("CollectionTemplateCard", () => {
  it("shows the template title, description, source badge, and media kind", () => {
    const markup = renderToStaticMarkup(
      <CollectionTemplateCard
        template={{ ...baseTemplate, poster_path: "/images/collection-templates/trending.jpg" }}
        onPick={() => {}}
      />,
    );

    expect(markup).toContain("Trending Movies This Week");
    expect(markup).toContain("Top trending movies on TMDB");
    expect(markup).toContain("TMDB");
    expect(markup).toContain("Movies");
    expect(markup).toContain("syncs daily");
    expect(markup).not.toContain("<img");
  });

  it("badges templates that require a profile", () => {
    const markup = renderToStaticMarkup(
      <CollectionTemplateCard
        template={{
          ...baseTemplate,
          id: "trakt_recommended_movies",
          title: "Trakt Recommended Movies",
          source: "trakt",
          requires_profile: true,
          tmdb: undefined,
          trakt: { preset: "recommended", media_type: "movie" },
        }}
        onPick={() => {}}
      />,
    );

    expect(markup).toContain("Profile");
    expect(markup).toContain("Trakt");
  });

  it("falls back gracefully when no schedule is configured", () => {
    const markup = renderToStaticMarkup(
      <CollectionTemplateCard
        template={{ ...baseTemplate, default_sync_schedule: undefined }}
        onPick={() => {}}
      />,
    );

    expect(markup).not.toContain("syncs");
  });
});
