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

  it("creates a library with real-time monitoring switched off", () => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    act(() => {
      result.current.setName("Movies");
      result.current.updatePath(0, "/media");
      result.current.setRealtimeMonitoring(false);
    });
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({ realtime_monitoring: false });
  });

  it.each([false, true])("preserves an existing real-time monitoring switch of %s", (enabled) => {
    const library = {
      id: 1,
      name: "Movies",
      type: "movies",
      paths: ["/media"],
      realtime_monitoring: enabled,
    } as Library;
    const { result } = renderHook(() => useLibraryForm({ library }));
    act(() => {
      result.current.setName("Films");
    });
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({
      id: 1,
      body: { name: "Films", realtime_monitoring: enabled },
    });
  });
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

describe("seek preview switch", () => {
  const movies = { id: 1, name: "Movies", type: "movies", paths: ["/media"] } as Library;

  it("leaves the setting out of a save that does not change it", () => {
    const { result } = renderHook(() =>
      useLibraryForm({ library: { ...movies, trickplay_enabled: true } }),
    );
    act(() => result.current.setName("Films"));
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0].body).not.toHaveProperty("trickplay_enabled");
  });

  it("leaves the setting out of a new library that keeps the default", () => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    act(() => {
      result.current.setName("Movies");
      result.current.updatePath(0, "/media");
    });
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({ realtime_monitoring: true });
    expect(mutate.mock.calls[0]![0]).not.toHaveProperty("trickplay_enabled");
  });

  it("sends a change", () => {
    const { result } = renderHook(() => useLibraryForm({ library: movies }));
    act(() => result.current.setTrickplayEnabled(true));
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({ id: 1, body: { trickplay_enabled: true } });
  });

  it("turns previews off when the library stops holding video", () => {
    const { result } = renderHook(() =>
      useLibraryForm({ library: { ...movies, trickplay_enabled: true } }),
    );
    act(() => result.current.handleTypeChange("audiobooks"));
    act(() => {
      result.current.submit();
    });
    expect(mutate.mock.calls[0]![0]).toMatchObject({ body: { trickplay_enabled: false } });
  });

  it("is hidden when the server does not offer seek previews", () => {
    const { result } = renderHook(() => useLibraryForm({ library: movies }));
    render(<AdvancedFields form={result.current} chapterThumbnailsSupported />);
    expect(screen.queryByText("Generate seek previews")).toBeNull();
  });

  it("needs public asset storage to turn on, but can always turn off", () => {
    const off = renderHook(() => useLibraryForm({ library: movies }));
    const { unmount } = render(
      <AdvancedFields
        form={off.result.current}
        chapterThumbnailsSupported
        trickplaySupported={false}
      />,
    );
    expect(
      screen.getByText("Public asset storage is required before this can be enabled."),
    ).toBeTruthy();
    expect(screen.getByRole("switch", { name: /seek previews/i }).hasAttribute("disabled")).toBe(
      true,
    );
    unmount();

    const on = renderHook(() =>
      useLibraryForm({ library: { ...movies, trickplay_enabled: true } }),
    );
    render(
      <AdvancedFields
        form={on.result.current}
        chapterThumbnailsSupported
        trickplaySupported={false}
      />,
    );
    expect(screen.getByRole("switch", { name: /seek previews/i }).hasAttribute("disabled")).toBe(
      false,
    );
  });
});
