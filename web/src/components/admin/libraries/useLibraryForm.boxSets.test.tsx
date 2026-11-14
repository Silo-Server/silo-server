import { act, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Library } from "@/api/types";
import { libraryCreateToV2 } from "@/api/v2/libraries";
import { BoxSetFields } from "./LibraryFormSections";
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

const movies = { id: 1, name: "Movies", type: "movies", paths: ["/media"] } as Library;

function save(result: { current: ReturnType<typeof useLibraryForm> }) {
  act(() => {
    result.current.submit();
  });
  return mutate.mock.calls[0]![0];
}

describe("box set library settings", () => {
  it("leaves the box set settings out of a save that does not change them", () => {
    const { result } = renderHook(() => useLibraryForm({ library: movies }));
    const body = save(result).body;
    expect(body).not.toHaveProperty("box_sets_enabled");
    expect(body).not.toHaveProperty("box_set_min_movies");
    expect(body).not.toHaveProperty("box_sets_in_search");
  });

  it("turns box sets on with the chosen size and search setting", () => {
    const { result } = renderHook(() => useLibraryForm({ library: movies }));
    act(() => {
      result.current.setBoxSetsEnabled(true);
      result.current.setBoxSetMinMovies(3);
      result.current.setBoxSetsInSearch(false);
    });
    expect(save(result)).toMatchObject({
      id: 1,
      body: { box_sets_enabled: true, box_set_min_movies: 3, box_sets_in_search: false },
    });
  });

  it("turns box sets off without resending their other settings", () => {
    const { result } = renderHook(() =>
      useLibraryForm({ library: { ...movies, box_sets_enabled: true, box_set_min_movies: 3 } }),
    );
    act(() => {
      result.current.setBoxSetsEnabled(false);
    });
    const body = save(result).body;
    expect(body).toMatchObject({ box_sets_enabled: false });
    expect(body).not.toHaveProperty("box_set_min_movies");
  });

  it("never turns box sets on for a library without movies", () => {
    const { result } = renderHook(() => useLibraryForm({ library: { ...movies, type: "series" } }));
    act(() => {
      result.current.setBoxSetsEnabled(true);
    });
    expect(result.current.settingSupport.boxSets).toBe(false);
    expect(save(result).body).not.toHaveProperty("box_sets_enabled");
  });

  it("sends the box set settings on create", () => {
    expect(
      libraryCreateToV2({
        name: "Movies",
        paths: ["/media"],
        type: "movies",
        box_sets_enabled: true,
        box_set_min_movies: 4,
      }),
    ).toMatchObject({ box_sets_enabled: true, box_set_min_movies: 4 });
  });

  it("shows the size and search controls only while box sets are on", () => {
    const { result } = renderHook(() => useLibraryForm({ library: movies }));
    const { rerender } = render(<BoxSetFields form={result.current} />);
    expect(screen.queryByText("Minimum movies in a box set")).toBeNull();

    fireEvent.click(screen.getByRole("switch", { name: "Box sets" }));
    rerender(<BoxSetFields form={result.current} />);
    expect(screen.getByRole("radio", { name: "2" })).toHaveAttribute("aria-checked", "true");

    fireEvent.click(screen.getByRole("radio", { name: "4" }));
    rerender(<BoxSetFields form={result.current} />);
    expect(result.current.boxSetMinMovies).toBe(4);
    expect(screen.getByRole("switch", { name: "Show box sets in search" })).toBeChecked();
  });
});
