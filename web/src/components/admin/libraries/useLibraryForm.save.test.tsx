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

type LibraryForm = ReturnType<typeof useLibraryForm>;

/** Applies `edit` to a form for `library`, submits it, and returns what the save sent. */
function submitted(library: Library | null, edit: (form: LibraryForm) => void) {
  const { result } = renderHook(() => useLibraryForm({ library }));
  act(() => edit(result.current));
  act(() => {
    result.current.submit();
  });
  return mutate.mock.calls[0]![0];
}

function fillNew(form: LibraryForm, name = "Library") {
  form.setName(name);
  form.updatePath(0, "/media");
}

describe("saving library processing settings", () => {
  it.each([
    ["series", true],
    ["mixed", true],
    ["movies", false],
    ["audiobooks", false],
  ])("new %s libraries default intro detection to %s", (type, introDetectionEnabled) => {
    const body = submitted(null, (form) => {
      fillNew(form);
      form.handleTypeChange(type);
    });
    expect(body).toMatchObject({ type, intro_detection_enabled: introDetectionEnabled });
  });

  it.each([false, true])("preserves an existing intro detection setting of %s", (enabled) => {
    const library = {
      id: 1,
      name: "Series",
      type: "series",
      paths: ["/media"],
      intro_detection_enabled: enabled,
    } as Library;
    expect(submitted(library, (form) => form.setName("Renamed"))).toMatchObject({
      id: 1,
      body: { name: "Renamed", intro_detection_enabled: enabled },
    });
  });

  it.each([
    ["movies", true],
    ["series", false],
  ])("saves a new %s library with detection switched to %s", (type, enabled) => {
    const body = submitted(null, (form) => {
      fillNew(form);
      form.handleTypeChange(type);
      form.setIntroDetectionEnabled(enabled);
    });
    expect(body).toMatchObject({ type, intro_detection_enabled: enabled });
  });

  it("follows the type's default until the switch is set", () => {
    const { result } = renderHook(() => useLibraryForm({ library: null }));
    expect(result.current.introDetectionEnabled).toBe(false);
    act(() => result.current.handleTypeChange("series"));
    expect(result.current.introDetectionEnabled).toBe(true);
    act(() => result.current.handleTypeChange("movies"));
    expect(result.current.introDetectionEnabled).toBe(false);
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
    expect(submitted(library, (form) => form.setName("Renamed"))).toMatchObject({
      id: 1,
      body: { name: "Renamed", trailer_kinds: ["trailer"], chapter_thumbnails_enabled: true },
    });
  });

  it.each(["audiobooks", "podcasts"])(
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
      expect(submitted(library, (form) => form.handleTypeChange(type))).toMatchObject({
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
    const body = submitted(null, (form) => {
      fillNew(form, "Movies");
      form.setRealtimeMonitoring(false);
    });
    expect(body).toMatchObject({ realtime_monitoring: false });
  });

  it.each([false, true])("preserves an existing real-time monitoring switch of %s", (enabled) => {
    const library = {
      id: 1,
      name: "Movies",
      type: "movies",
      paths: ["/media"],
      realtime_monitoring: enabled,
    } as Library;
    expect(submitted(library, (form) => form.setName("Films"))).toMatchObject({
      id: 1,
      body: { name: "Films", realtime_monitoring: enabled },
    });
  });
});

describe("marker detection switch", () => {
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
    const saved = submitted({ ...movies, trickplay_enabled: true }, (form) =>
      form.setName("Films"),
    );
    expect(saved.body).not.toHaveProperty("trickplay_enabled");
  });

  it("leaves the setting out of a new library that keeps the default", () => {
    const body = submitted(null, (form) => fillNew(form, "Movies"));
    expect(body).toMatchObject({ realtime_monitoring: true });
    expect(body).not.toHaveProperty("trickplay_enabled");
  });

  it("sends a change", () => {
    expect(submitted(movies, (form) => form.setTrickplayEnabled(true))).toMatchObject({
      id: 1,
      body: { trickplay_enabled: true },
    });
  });

  it("turns previews off when the library stops holding video", () => {
    const saved = submitted({ ...movies, trickplay_enabled: true }, (form) =>
      form.handleTypeChange("audiobooks"),
    );
    expect(saved).toMatchObject({ body: { trickplay_enabled: false } });
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
