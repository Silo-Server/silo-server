import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useLibraryPageStatePreference } from "./libraryPageState";

const mocks = vi.hoisted(() => ({
  mutate: vi.fn(),
  mutateAsync: vi.fn(),
  profileId: "profile-1",
}));

vi.mock("@/utils/storage", () => ({
  storage: {
    KEYS: { PROFILE_ID: "profile_id" },
    get: () => mocks.profileId,
  },
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
    mocks.profileId = "profile-1";
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

  it("removes a rejected value before applying later queued writes", async () => {
    let rejectFirst: ((reason?: unknown) => void) | undefined;
    mocks.mutateAsync
      .mockImplementationOnce(
        () =>
          new Promise((_, reject) => {
            rejectFirst = reject;
          }),
      )
      .mockResolvedValueOnce({});
    const { result } = renderHook(() => useLibraryPageStatePreference());

    const rejected = result.current.saveLibrarySearch(3, "tab=library&sort=year");
    const rejectedError = rejected.catch((error: unknown) => error);
    const queued = result.current.saveLibrarySearch(9, "tab=collections");

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    await act(async () => {
      rejectFirst?.(new Error("rate limited"));
      expect(await rejectedError).toEqual(new Error("rate limited"));
      await queued;
    });

    expect(mocks.mutateAsync).toHaveBeenCalledTimes(2);
    expect(mocks.mutateAsync.mock.calls[1][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "9": { search: "tab=collections" },
      },
    });
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
    const tailError = tail.catch((error: unknown) => error);
    const coalescedError = coalesced.catch((error: unknown) => error);

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    await act(async () => {
      resolveFirst?.({});
      await first;
    });

    expect(await tailError).toEqual(new Error("rate limited"));
    expect(await coalescedError).toEqual(new Error("rate limited"));
    expect(mocks.mutateAsync).toHaveBeenCalledTimes(2);
  });

  it("cancels queued writes when the active profile changes", async () => {
    let resolveFirst: ((value: unknown) => void) | undefined;
    mocks.mutateAsync.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFirst = resolve;
        }),
    );
    const { result, rerender } = renderHook(() => useLibraryPageStatePreference());

    const first = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const queued = result.current.saveLibrarySearch(9, "tab=collections");
    const queuedError = queued.catch((error: unknown) => error);

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    act(() => {
      mocks.profileId = "profile-2";
      rerender();
    });
    await act(async () => {
      resolveFirst?.({});
      await first;
    });

    expect(await queuedError).toEqual(
      new Error("Library preference write cancelled because the active profile changed"),
    );
    expect(mocks.mutateAsync).toHaveBeenCalledTimes(1);
  });
});
