import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { ResolvedSection } from "@/api/types";
import {
  buildLibraryCollectionCatalogHref,
  buildUserCollectionCatalogHref,
} from "@/pages/catalogSearchParams";
import SectionRow from "./SectionRow";

vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p-other", name: "Alex" }] }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-me", name: "Sam" }, isLoading: false }),
}));
vi.mock("@/hooks/queries/sidebarPins", () => ({
  useToggleSidebarPin: () => ({ togglePin: vi.fn(), isPinned: () => false, canToggle: true }),
}));

const section: ResolvedSection = {
  id: "franchises",
  section_type: "collection_posters",
  title: "Franchises",
  featured: false,
  item_limit: 20,
  total_count: 2,
  is_custom: false,
  customized: false,
  items: [],
  collections: [
    {
      id: "marvel",
      source: "library",
      title: "Marvel",
      poster_url: "https://images.example.test/marvel.jpg",
      poster_is_collage: false,
      item_count: 12,
      library_id: 3,
    },
    {
      id: "picks",
      source: "user",
      title: "Alex's picks",
      poster_url: "",
      poster_is_collage: false,
      item_count: 4,
      creator_profile_id: "p-other",
    },
  ],
};

function wrap(node: ReactNode) {
  return (
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>
  );
}

function escaped(href: string): string {
  return href.replaceAll("&", "&amp;");
}

describe("a row of collections", () => {
  it("shows one card per collection, each opening its collection, with no Explore all or pins", () => {
    const markup = renderToStaticMarkup(wrap(<SectionRow section={section} libraryId={3} />));
    expect(markup).toContain(">Franchises<");
    expect(markup).toContain(
      `href="${escaped(buildLibraryCollectionCatalogHref("marvel", "Marvel", 3))}"`,
    );
    expect(markup).toContain(
      `href="${escaped(buildUserCollectionCatalogHref("picks", "Alex's picks"))}"`,
    );
    expect(markup).toContain("by Alex");
    expect(markup).not.toContain("Explore all");
    expect(markup).not.toContain("Pin to sidebar");
  });

  it("renders nothing when the viewer sees none of its collections", () => {
    const markup = renderToStaticMarkup(
      wrap(<SectionRow section={{ ...section, collections: [] }} />),
    );
    expect(markup).toBe("");
  });
});
