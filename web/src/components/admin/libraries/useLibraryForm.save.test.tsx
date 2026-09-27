import { act, render, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Library } from "@/api/types";
import { AdvancedFields } from "./LibraryFormSections";
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

  it.each([
    ["movies", true],
    ["series", false],
  ])("saves a new %s library with detection switched to %s", (type, enabled) => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    act(() => {
      result.current.setName("Library");
      result.current.updatePath(0, "/media");
      result.current.handleTypeChange(type);
      result.current.setIntroDetectionEnabled(enabled);
    });
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({ type, intro_detection_enabled: enabled });
  });

  it("follows the type's default until the switch is set", () => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    expect(result.current.introDetectionEnabled).toBe(false);
    act(() => result.current.handleTypeChange("series"));
    expect(result.current.introDetectionEnabled).toBe(true);
    act(() => result.current.handleTypeChange("movies"));
    expect(result.current.introDetectionEnabled).toBe(false);
  });

  it.each([false, true])(
    "preserves an existing movie library's detection setting of %s",
    (enabled) => {
      const library = {
        id: 2,
        name: "Movies",
        type: "movies",
        paths: ["/media"],
        intro_detection_enabled: enabled,
      } as Library;
      const { result } = renderHook(() => useLibraryForm({ library }));
      act(() => {
        result.current.setName("Films");
      });
      act(() => {
        result.current.submit();
      });
      expect(mutate.mock.calls[0]![0]).toMatchObject({
        id: 2,
        body: { name: "Films", intro_detection_enabled: enabled },
      });
    },
  );

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

describe("marker detection switch", () => {
  it.each([
    ["movies", "Detect credits markers (best effort)"],
    ["series", "Detect intro and credits markers"],
    ["mixed", "Detect intro and credits markers"],
  ])("labels detection for %s libraries", (type, label) => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    act(() => result.current.handleTypeChange(type));
    render(<AdvancedFields form={result.current} chapterThumbnailsSupported={false} />);
    expect(screen.getByText(label)).toBeTruthy();
  });

  it.each([
    ["movies", /^Looks for end credits in movies.* Needs Detect credits on in server settings\.$/],
    [
      "series",
      /episodes in this library\. Embedded intro and credits chapters are used when available\. Detect intros and Detect credits in server settings choose which markers it finds\.$/,
    ],
    [
      "mixed",
      /episodes in this library\..* Movies get end credits only, on a best-effort basis\. Detect intros and Detect credits/,
    ],
  ])("describes what detection covers in %s libraries", (type, description) => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    act(() => result.current.handleTypeChange(type));
    render(<AdvancedFields form={result.current} chapterThumbnailsSupported={false} />);
    expect(screen.getByText(description)).toBeTruthy();
  });

  it("is hidden for book libraries", () => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    act(() => result.current.handleTypeChange("audiobooks"));
    render(<AdvancedFields form={result.current} chapterThumbnailsSupported={false} />);
    expect(screen.queryByText(/Detect .*markers/)).toBeNull();
  });
});
