import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useLibraryPageStatePreference } from "./libraryPageState";

const mocks = vi.hoisted(() => ({
  mutate: vi.fn(),
  mutateAsync: vi.fn(),
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: () => ({
    data: {
      "ui.library_page_state": {
        value: { version: 1, libraries: { "3": { search: "tab=collections" } } },
      },
      "ui.remember_library_page_state": { value: true },
    },
    isLoading: false,
  }),
  useSetSettingValue: () => ({
    mutate: mocks.mutate,
    mutateAsync: mocks.mutateAsync,
  }),
}));

describe("useLibraryPageStatePreference", () => {
  beforeEach(() => {
    mocks.mutate.mockReset();
    mocks.mutateAsync.mockReset();
  });

  it("serializes whole-document saves and preserves queued library changes", async () => {
    let resolveFirst: ((value: unknown) => void) | undefined;
    mocks.mutateAsync
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveFirst = resolve;
          }),
      )
      .mockResolvedValueOnce({});
    const { result } = renderHook(() => useLibraryPageStatePreference());

    act(() => {
      void result.current.saveLibrarySearch(7, "tab=library&sort=year");
      void result.current.saveLibrarySearch(9, "tab=collections");
    });

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    expect(mocks.mutate).not.toHaveBeenCalled();

    await act(async () => {
      resolveFirst?.({});
      await Promise.resolve();
    });
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(2));

    expect(mocks.mutateAsync.mock.calls[0][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
      },
    });
    expect(mocks.mutateAsync.mock.calls[1][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
        "9": { search: "tab=collections" },
      },
    });
  });

  it("allows the same desired state to retry after a failed write", async () => {
    mocks.mutateAsync.mockRejectedValueOnce(new Error("rate limited")).mockResolvedValueOnce({});
    const { result } = renderHook(() => useLibraryPageStatePreference());

    await act(async () => {
      await expect(result.current.saveLibrarySearch(7, "tab=library&sort=year")).rejects.toThrow(
        "rate limited",
      );
    });
    await act(async () => {
      await result.current.saveLibrarySearch(7, "tab=library&sort=year");
    });

    expect(mocks.mutateAsync).toHaveBeenCalledTimes(2);
  });

  it("preserves a queued tail failure for a coalesced save", async () => {
    let resolveFirst: ((value: unknown) => void) | undefined;
    mocks.mutateAsync
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveFirst = resolve;
          }),
      )
      .mockRejectedValueOnce(new Error("rate limited"));
    const { result } = renderHook(() => useLibraryPageStatePreference());

    const first = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const tail = result.current.saveLibrarySearch(9, "tab=collections");
    const coalesced = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const tailRejection = expect(tail).rejects.toThrow("rate limited");
    const coalescedRejection = expect(coalesced).rejects.toThrow("rate limited");

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    await act(async () => {
      resolveFirst?.({});
      await first;
    });

    await tailRejection;
    await coalescedRejection;
    expect(mocks.mutateAsync).toHaveBeenCalledTimes(2);
  });
});
