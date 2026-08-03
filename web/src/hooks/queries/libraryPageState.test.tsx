import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiClientError } from "@/api/client";
import {
  libraryPageStateWriteRetryDelay,
  shouldRetryLibraryPageStateWrite,
  useLibraryPageStatePreference,
} from "./libraryPageState";

const mocks = vi.hoisted(() => ({
  mutate: vi.fn(),
  mutateAsync: vi.fn(),
  profileId: "profile-1",
  preference: {
    version: 1 as const,
    libraries: { "3": { search: "tab=collections" } } as Record<string, { search: string }>,
  },
}));

vi.mock("@/utils/storage", () => ({
  storage: {
    KEYS: { PROFILE_ID: "profile_id" },
    get: () => mocks.profileId,
  },
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  isDefinitiveSettingMutationRejection: (error: unknown) => {
    const status =
      typeof error === "object" && error !== null && "status" in error
        ? (error as { status?: unknown }).status
        : undefined;
    return typeof status === "number" && status >= 400 && status < 500 && status !== 408;
  },
  useEffectiveSettings: () => ({
    data: {
      "ui.library_page_state": {
        value: mocks.preference,
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
    mocks.preference = {
      version: 1,
      libraries: { "3": { search: "tab=collections" } },
    };
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
    mocks.mutateAsync
      .mockRejectedValueOnce(new ApiClientError(429, "rate_limited", "rate limited"))
      .mockResolvedValueOnce({});
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

  it("retries the same desired state after an ambiguous write failure", async () => {
    mocks.mutateAsync
      .mockRejectedValueOnce(new TypeError("network connection lost"))
      .mockResolvedValueOnce({});
    const { result } = renderHook(() => useLibraryPageStatePreference());

    await act(async () => {
      await expect(result.current.saveLibrarySearch(7, "tab=library&sort=year")).rejects.toThrow(
        "network connection lost",
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
      rejectFirst?.(new ApiClientError(429, "rate_limited", "rate limited"));
      expect(await rejectedError).toEqual(new ApiClientError(429, "rate_limited", "rate limited"));
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

  it("preserves an ambiguous attempted write before advancing the queue", async () => {
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

    const ambiguous = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const ambiguousError = ambiguous.catch((error: unknown) => error);
    const queued = result.current.saveLibrarySearch(9, "tab=collections");

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    await act(async () => {
      rejectFirst?.(new TypeError("response connection lost"));
      expect(await ambiguousError).toEqual(new TypeError("response connection lost"));
      await queued;
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

  it("treats a server error as ambiguous before advancing the queue", async () => {
    mocks.mutateAsync
      .mockRejectedValueOnce(new ApiClientError(503, "unavailable", "service unavailable"))
      .mockResolvedValueOnce({});
    const { result } = renderHook(() => useLibraryPageStatePreference());

    const ambiguous = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const ambiguousError = ambiguous.catch((error: unknown) => error);
    const queued = result.current.saveLibrarySearch(9, "tab=collections");

    await ambiguousError;
    await queued;

    expect(mocks.mutateAsync.mock.calls[1][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
        "9": { search: "tab=collections" },
      },
    });
  });

  it("merges an ambiguous write onto a deferred settings snapshot", async () => {
    let rejectFirst: ((reason?: unknown) => void) | undefined;
    mocks.mutateAsync
      .mockImplementationOnce(
        () =>
          new Promise((_, reject) => {
            rejectFirst = reject;
          }),
      )
      .mockResolvedValueOnce({});
    const { result, rerender } = renderHook(() => useLibraryPageStatePreference());

    const ambiguous = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const ambiguousError = ambiguous.catch((error: unknown) => error);
    const queued = result.current.saveLibrarySearch(9, "tab=collections");

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    act(() => {
      mocks.preference = {
        version: 1,
        libraries: {
          "3": { search: "tab=collections" },
          "11": { search: "tab=library&sort=title" },
        },
      };
      rerender();
    });
    await act(async () => {
      rejectFirst?.(new TypeError("response connection lost"));
      await ambiguousError;
      await queued;
    });

    expect(mocks.mutateAsync.mock.calls[1][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
        "9": { search: "tab=collections" },
        "11": { search: "tab=library&sort=title" },
      },
    });
  });

  it("merges a successful write onto a deferred settings snapshot", async () => {
    let resolveFirst: ((value: unknown) => void) | undefined;
    mocks.mutateAsync
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveFirst = resolve;
          }),
      )
      .mockResolvedValueOnce({});
    const { result, rerender } = renderHook(() => useLibraryPageStatePreference());

    const first = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const queued = result.current.saveLibrarySearch(9, "tab=collections");

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    act(() => {
      mocks.preference = {
        version: 1,
        libraries: {
          "3": { search: "tab=collections" },
          "11": { search: "tab=library&sort=title" },
        },
      };
      rerender();
    });
    await act(async () => {
      resolveFirst?.({});
      await first;
      await queued;
    });

    expect(mocks.mutateAsync.mock.calls[1][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
        "9": { search: "tab=collections" },
        "11": { search: "tab=library&sort=title" },
      },
    });
  });

  it("keeps an earlier ambiguous edit when a stale snapshot arrives during the next write", async () => {
    let rejectFirst: ((reason?: unknown) => void) | undefined;
    let resolveSecond: ((value: unknown) => void) | undefined;
    mocks.mutateAsync
      .mockImplementationOnce(
        () =>
          new Promise((_, reject) => {
            rejectFirst = reject;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveSecond = resolve;
          }),
      )
      .mockResolvedValueOnce({});
    const { result, rerender } = renderHook(() => useLibraryPageStatePreference());

    const first = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const firstError = first.catch((error: unknown) => error);
    const second = result.current.saveLibrarySearch(9, "tab=collections");

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    await act(async () => {
      rejectFirst?.(new TypeError("response connection lost"));
      await firstError;
    });
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(2));

    act(() => {
      mocks.preference = {
        version: 1,
        libraries: {
          "3": { search: "tab=collections" },
          "11": { search: "tab=library&sort=title" },
        },
      };
      rerender();
    });
    await act(async () => {
      resolveSecond?.({});
      await second;
    });
    await act(async () => {
      await result.current.saveLibrarySearch(13, "tab=library&sort=added");
    });

    expect(mocks.mutateAsync.mock.calls[2][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
        "9": { search: "tab=collections" },
        "11": { search: "tab=library&sort=title" },
        "13": { search: "tab=library&sort=added" },
      },
    });
  });

  it("keeps an earlier successful edit when a stale snapshot arrives during the next write", async () => {
    let resolveFirst: ((value: unknown) => void) | undefined;
    let resolveSecond: ((value: unknown) => void) | undefined;
    mocks.mutateAsync
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveFirst = resolve;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveSecond = resolve;
          }),
      )
      .mockResolvedValueOnce({});
    const { result, rerender } = renderHook(() => useLibraryPageStatePreference());

    const first = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const second = result.current.saveLibrarySearch(9, "tab=collections");

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    await act(async () => {
      resolveFirst?.({});
      await first;
    });
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(2));

    act(() => {
      mocks.preference = {
        version: 1,
        libraries: {
          "3": { search: "tab=collections" },
          "11": { search: "tab=library&sort=title" },
        },
      };
      rerender();
    });
    await act(async () => {
      resolveSecond?.({});
      await second;
    });
    await act(async () => {
      await result.current.saveLibrarySearch(13, "tab=library&sort=added");
    });

    expect(mocks.mutateAsync.mock.calls[2][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
        "9": { search: "tab=collections" },
        "11": { search: "tab=library&sort=title" },
        "13": { search: "tab=library&sort=added" },
      },
    });
  });

  it("keeps an ambiguous edit when reconciliation finishes before the next write", async () => {
    mocks.mutateAsync
      .mockRejectedValueOnce(new TypeError("response connection lost"))
      .mockResolvedValueOnce({});
    const { result, rerender } = renderHook(() => useLibraryPageStatePreference());

    await act(async () => {
      await expect(result.current.saveLibrarySearch(7, "tab=library&sort=year")).rejects.toThrow(
        "response connection lost",
      );
    });
    act(() => {
      mocks.preference = {
        version: 1,
        libraries: {
          "3": { search: "tab=collections" },
          "11": { search: "tab=library&sort=title" },
        },
      };
      rerender();
    });
    await act(async () => {
      await result.current.saveLibrarySearch(9, "tab=collections");
    });

    expect(mocks.mutateAsync.mock.calls[1][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
        "9": { search: "tab=collections" },
        "11": { search: "tab=library&sort=title" },
      },
    });
  });

  it("applies a deferred settings snapshot after a definitive write failure", async () => {
    let rejectFirst: ((reason?: unknown) => void) | undefined;
    mocks.mutateAsync
      .mockImplementationOnce(
        () =>
          new Promise((_, reject) => {
            rejectFirst = reject;
          }),
      )
      .mockResolvedValueOnce({});
    const { result, rerender } = renderHook(() => useLibraryPageStatePreference());

    const rejected = result.current.saveLibrarySearch(7, "tab=library&sort=year");
    const rejectedError = rejected.catch((error: unknown) => error);
    const queued = result.current.saveLibrarySearch(9, "tab=collections");

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    act(() => {
      mocks.preference = {
        version: 1,
        libraries: {
          "3": { search: "tab=collections" },
          "11": { search: "tab=library&sort=title" },
        },
      };
      rerender();
    });
    await act(async () => {
      rejectFirst?.(new ApiClientError(429, "rate_limited", "rate limited"));
      await rejectedError;
      await queued;
    });

    expect(mocks.mutateAsync.mock.calls[1][0].value).toEqual({
      version: 1,
      libraries: {
        "3": { search: "tab=collections" },
        "9": { search: "tab=collections" },
        "11": { search: "tab=library&sort=title" },
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
      .mockRejectedValueOnce(new ApiClientError(429, "rate_limited", "rate limited"));
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

    expect(await tailError).toEqual(new ApiClientError(429, "rate_limited", "rate limited"));
    expect(await coalescedError).toEqual(new ApiClientError(429, "rate_limited", "rate limited"));
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

    const cancellation = await queuedError;
    expect(cancellation).toEqual(
      new Error("Library preference write cancelled because the active profile changed"),
    );
    expect(shouldRetryLibraryPageStateWrite(cancellation)).toBe(true);
    expect(libraryPageStateWriteRetryDelay(cancellation, 2_000)).toBe(0);
    expect(mocks.mutateAsync).toHaveBeenCalledTimes(1);
  });

  it("honors a rate-limit retry hint while keeping retries bounded by the caller", () => {
    const error = new ApiClientError(429, "rate_limit_exceeded", "rate limited");
    error.body = { retry_after: 30 };

    expect(shouldRetryLibraryPageStateWrite(error)).toBe(true);
    expect(libraryPageStateWriteRetryDelay(error, 2_000)).toBe(30_000);
    expect(
      libraryPageStateWriteRetryDelay(
        new ApiClientError(422, "validation_failed", "invalid setting"),
        2_000,
      ),
    ).toBeNull();
  });
});
