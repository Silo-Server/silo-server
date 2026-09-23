import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Library } from "@/api/types";
import { useLibraryForm } from "./useLibraryForm";

const { mutate } = vi.hoisted(() => ({ mutate: vi.fn() }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useCreateLibrary: () => ({ mutate, isPending: false }),
  useUpdateLibrary: () => ({ mutate, isPending: false }),
  useSetLibraryProviders: () => ({ mutate: vi.fn(), isPending: false }),
  useLibraryProviders: () => ({ data: { levels: {} } }),
  useLibraryProviderDefaults: () => ({ data: { levels: {} }, isLoading: false }),
}));

beforeEach(() => mutate.mockClear());

describe("saving library processing settings", () => {
  it.each([
    ["series", true],
    ["mixed", true],
    ["movies", false],
    ["audiobooks", false],
    ["ebooks", false],
    ["manga", false],
    ["podcasts", false],
  ])("new %s libraries default intro detection to %s", (type, introDetectionEnabled) => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    act(() => {
      result.current.setName("Library");
      result.current.updatePath(0, "/media");
      result.current.handleTypeChange(type);
    });
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({
      type,
      intro_detection_enabled: introDetectionEnabled,
    });
  });

  it.each([false, true])("preserves an existing intro detection setting of %s", (enabled) => {
    const library = {
      id: 1,
      name: "Series",
      type: "series",
      paths: ["/media"],
      intro_detection_enabled: enabled,
    } as Library;
    const { result } = renderHook(() => useLibraryForm({ library }));
    act(() => {
      result.current.setName("Renamed");
    });
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({
      id: 1,
      body: { name: "Renamed", intro_detection_enabled: enabled },
    });
  });

  it("preserves disabling intro detection while creating a library", () => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    act(() => {
      result.current.setName("Series");
      result.current.updatePath(0, "/media");
      result.current.handleTypeChange("series");
      result.current.setIntroDetectionEnabled(false);
    });
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({
      type: "series",
      intro_detection_enabled: false,
    });
  });

  it.each(["homevideos", "shows"])("preserves video settings for %s", (type) => {
    const library = {
      id: 1,
      name: "Videos",
      type,
      paths: ["/media"],
      trailer_kinds: ["trailer"],
      chapter_thumbnails_enabled: true,
    } as Library;
    const { result } = renderHook(() => useLibraryForm({ library }));
    act(() => {
      result.current.setName("Renamed");
    });
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({
      id: 1,
      body: { name: "Renamed", trailer_kinds: ["trailer"], chapter_thumbnails_enabled: true },
    });
  });

  it.each(["audiobooks", "ebooks", "manga", "podcasts"])(
    "disables stale video settings after changing to %s",
    (type) => {
      const library = {
        id: 1,
        name: "Library",
        type: "series",
        paths: ["/media"],
        trailer_kinds: ["trailer"],
        chapter_thumbnails_enabled: true,
        intro_detection_enabled: true,
      } as Library;
      const { result } = renderHook(() => useLibraryForm({ library }));
      act(() => {
        result.current.handleTypeChange(type);
      });
      act(() => {
        result.current.submit();
      });
      expect(mutate.mock.calls[0]![0]).toMatchObject({
        id: 1,
        body: {
          type,
          trailer_kinds: [],
          chapter_thumbnails_enabled: false,
          intro_detection_enabled: false,
        },
      });
    },
  );
});

it("saves the selected certification country on an existing library", () => {
  const library = {
    id: 1,
    name: "Movies",
    type: "movies",
    paths: ["/media"],
    certification_country: "US",
  } as Library;
  const { result } = renderHook(() => useLibraryForm({ library }));
  act(() => result.current.setCertificationCountry("AU"));
  act(() => result.current.submit());
  expect(mutate.mock.calls[0]![0]).toMatchObject({
    id: 1,
    body: { certification_country: "AU", refresh_metadata_on_country_change: false },
  });
});

it("only requests a whole-library refresh after opting in", () => {
  const library = {
    id: 1,
    name: "Movies",
    type: "movies",
    paths: ["/media"],
    certification_country: "US",
  } as Library;
  const { result } = renderHook(() => useLibraryForm({ library }));
  act(() => {
    result.current.setCertificationCountry("AU");
    result.current.setRefreshMetadataOnCountryChange(true);
  });
  act(() => result.current.submit());
  expect(mutate.mock.calls[0]![0]).toMatchObject({
    id: 1,
    body: { certification_country: "AU", refresh_metadata_on_country_change: true },
  });
});
