import { act, render, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Library, LibraryProviderChainResponse } from "@/api/types";
import { MetadataFields } from "./LibraryFormSections";
import { levelChainsFromResponse, useLibraryForm } from "./useLibraryForm";

const entry = (id: number, slug: string, priority: number, enabled: boolean, local = false) => ({
  plugin_installation_id: id,
  capability_id: slug,
  provider_slug: slug,
  priority,
  enabled,
  local,
});

// A series library upgraded from an older release: the server lists local
// metadata first at each level.
const savedChain: LibraryProviderChainResponse = {
  levels: {
    series: [entry(1, "nfo", 0, false, true), entry(3, "tvdb", 1, true), entry(2, "tmdb", 2, true)],
    season: [entry(1, "nfo", 0, false, true), entry(3, "tvdb", 1, true), entry(2, "tmdb", 2, true)],
    episode: [entry(1, "nfo", 0, true, true), entry(3, "tvdb", 1, true), entry(2, "tmdb", 2, true)],
  },
};

const { setChain } = vi.hoisted(() => ({ setChain: vi.fn() }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useCreateLibrary: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateLibrary: () => ({
    mutate: (_: unknown, options?: { onSuccess?: () => void }) => options?.onSuccess?.(),
    isPending: false,
  }),
  useSetLibraryProviders: () => ({ mutate: setChain, isPending: false }),
  useLibraryProviders: () => ({ data: savedChain }),
  useLibraryProviderDefaults: () => ({ data: { levels: {} }, isLoading: false }),
  useLibraryRealtimeMonitoring: () => ({ data: undefined }),
}));

// The translation panel fetches its own status; this file only covers the provider chain.
vi.mock("./LibraryTranslatePanel", () => ({ LibraryTranslatePanel: () => null }));

beforeEach(() => setChain.mockClear());

const library = { id: 7, name: "Shows", type: "series", paths: ["/tv"] } as Library;

describe("local metadata in the provider chain", () => {
  it("lists local metadata first even when the response orders it later", () => {
    const chains = levelChainsFromResponse({
      levels: { movie: [entry(2, "tmdb", 0, true), entry(1, "nfo", 1, true, true)] },
    });
    expect(chains["movie"]!.map((item) => item.provider_slug)).toEqual(["nfo", "tmdb"]);
  });

  it("shows one switch, on when any level reads local files, and keeps it out of the level lists", () => {
    const { result } = renderHook(() => useLibraryForm({ library }));
    expect(result.current.hasLocalMetadata).toBe(true);
    expect(result.current.localMetadataEnabled).toBe(true);
    expect(result.current.remoteLevelChains["series"]!.map((item) => item.provider_slug)).toEqual([
      "tvdb",
      "tmdb",
    ]);
  });

  it("switches every level at once and saves local metadata first", () => {
    const { result } = renderHook(() => useLibraryForm({ library }));
    act(() => result.current.toggleLocalMetadata(false));
    expect(result.current.localMetadataEnabled).toBe(false);
    act(() => result.current.toggleLocalMetadata(true));
    // Reordering remote providers cannot move local metadata.
    act(() => {
      const [tvdb, tmdb] = result.current.remoteLevelChains["series"]!;
      result.current.reorderLevel("series", [tmdb!, tvdb!]);
    });
    act(() => result.current.toggleLevelProvider("season", 0));
    act(() => {
      result.current.submit();
    });

    const body = setChain.mock.calls[0]![0].body;
    for (const level of ["series", "season", "episode"]) {
      expect(body.levels[level][0]).toMatchObject({
        capability_id: "nfo",
        priority: 0,
        enabled: true,
      });
    }
    expect(body.levels.series.map((e: { capability_id: string }) => e.capability_id)).toEqual([
      "nfo",
      "tmdb",
      "tvdb",
    ]);
    expect(body.levels.season[1]).toMatchObject({ capability_id: "tvdb", enabled: false });
  });

  it("renders one labelled switch instead of a raw nfo row", () => {
    const { result } = renderHook(() => useLibraryForm({ library }));
    render(<MetadataFields form={result.current} />);
    const toggle = screen.getByRole("switch", { name: "Local metadata (NFO + artwork)" });
    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(screen.queryByText("nfo")).not.toBeInTheDocument();
  });
});
