// TEMPORARY verification harness — delete before commit.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import SectionRow from "@/components/SectionRow";
import ItemGrid from "@/components/ItemGrid";
import type { BrowseItem, ResolvedSection, SectionItem } from "@/api/types";

const client = new QueryClient({ defaultOptions: { queries: { enabled: false, retry: false } } });
const hues = [12, 48, 96, 160, 200, 240, 280, 320];
const poster = (title: string, i: number) =>
  `data:image/svg+xml;utf8,${encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="300" height="450"><rect width="300" height="450" fill="hsl(${hues[i % hues.length]} 45% 32%)"/><text x="150" y="230" font-family="sans-serif" font-size="34" fill="white" text-anchor="middle">${title}</text></svg>`,
  )}`;
const names = ["Heat", "Ronin", "Thief", "Drive", "Collateral", "Sicario", "Prisoners", "Arrival", "Dune", "Alien", "Aliens", "Brazil", "Fargo", "Memento", "Se7en", "Zodiac", "Gattaca", "Blade", "Akira", "Paprika"];
const titleAt = (i: number) => `${names[i % names.length]} ${Math.floor(i / names.length) + 1}`;
const sectionItems = (offset: number): SectionItem[] =>
  Array.from({ length: 30 }, (_, i) => ({
    content_id: `m-${offset + i}`, type: "movie", title: titleAt(offset + i), year: 1990 + ((offset + i) % 30),
    poster_url: poster(titleAt(offset + i), offset + i), status: "matched",
  }) as unknown as SectionItem);
const section = (id: string, title: string, offset: number): ResolvedSection =>
  ({ id, title, section_type: "recently_added", items: sectionItems(offset), total_count: 30, item_limit: 30 }) as unknown as ResolvedSection;
const gridItems: BrowseItem[] = Array.from({ length: 400 }, (_, i) => ({
  content_id: `g-${i}`, type: "movie", title: titleAt(i), year: 1990 + (i % 30), poster_url: poster(titleAt(i), i), status: "matched",
}) as unknown as BrowseItem);

export default function KbnavHarness() {
  const grid = new URLSearchParams(window.location.search).has("grid");
  return (
    <QueryClientProvider client={client}>
      <main className="bg-background text-foreground min-h-screen py-8">
        <button className="mx-10 mb-6 rounded border px-3 py-1">Before rows</button>
        {grid ? (
          <div className="px-10"><h2 className="mb-4 text-xl font-semibold">Library</h2><ItemGrid items={gridItems} /></div>
        ) : (
          <div className="flex flex-col gap-10">
            <SectionRow section={section("a", "Recently Added", 0)} priority />
            <SectionRow section={section("b", "Trending", 30)} />
          </div>
        )}
      </main>
    </QueryClientProvider>
  );
}
