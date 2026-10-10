import { render, screen } from "@testing-library/react";
import { RadioGroup } from "radix-ui";
import { describe, expect, it } from "vitest";

import type { CollectionTemplate } from "@/lib/collectionTemplates";

import { CollectionTemplateCard } from "./CollectionTemplateCard";

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

function show(template: CollectionTemplate, value = "") {
  render(
    <RadioGroup.Root aria-label="Lists" value={value}>
      <CollectionTemplateCard template={template} />
    </RadioGroup.Root>,
  );
  return screen.getByRole("radio", { name: template.title });
}

describe("CollectionTemplateCard", () => {
  it("is a radio named by the template, described by its source, kind, size and schedule", () => {
    const radio = show({
      ...baseTemplate,
      poster_path: "/images/collection-templates/trending.jpg",
    });
    expect(radio).toHaveAccessibleDescription("TMDB Movies · 50 titles · syncs daily");
    expect(radio).not.toBeChecked();
    expect(radio.querySelector("img")).toHaveAttribute(
      "src",
      "/images/collection-templates/trending.jpg",
    );
  });

  it("shows as checked when the draft follows it", () => {
    expect(show(baseTemplate, baseTemplate.id)).toBeChecked();
  });

  it("leaves out a schedule it doesn't have, and uses its icon without a poster", () => {
    const radio = show({ ...baseTemplate, default_sync_schedule: undefined, default_limit: 0 });
    expect(radio).toHaveAccessibleDescription("TMDB Movies");
    expect(radio).toHaveTextContent("🎬");
  });
});
