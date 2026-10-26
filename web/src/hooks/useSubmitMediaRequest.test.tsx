import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { RequestMediaResult } from "@/api/types";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/api/v2/requests", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/requests")>()),
  createMediaRequestV2: (...args: unknown[]) => mocks.create(...args),
}));
vi.mock("sonner", () => ({
  toast: { error: mocks.toastError, success: mocks.toastSuccess },
}));

import { useSubmitMediaRequest } from "./useSubmitMediaRequest";

function result(tmdbID: number, title: string): RequestMediaResult {
  return {
    media_type: "movie",
    tmdb_id: tmdbID,
    title,
    availability: "missing",
    request: { requestable: true },
  };
}

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

describe("useSubmitMediaRequest", () => {
  beforeEach(() => {
    mocks.create.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("clears each card's pending state when overlapping requests both fail", async () => {
    const rejects: Array<(error: Error) => void> = [];
    mocks.create.mockImplementation(() => new Promise((_resolve, reject) => rejects.push(reject)));
    const heat = result(1, "Heat");
    const ronin = result(2, "Ronin");
    const { result: hook } = renderHook(() => useSubmitMediaRequest(), { wrapper });

    act(() => {
      hook.current.submit(heat);
      hook.current.submit(ronin);
    });
    await waitFor(() => expect(rejects).toHaveLength(2));
    expect(hook.current.isSubmitting(heat)).toBe(true);
    expect(hook.current.isSubmitting(ronin)).toBe(true);

    // The first call fails while the second is still in flight.
    await act(async () => rejects[0]!(new Error("quota reached")));
    await waitFor(() => expect(hook.current.isSubmitting(heat)).toBe(false));
    expect(hook.current.isSubmitting(ronin)).toBe(true);

    await act(async () => rejects[1]!(new Error("quota reached")));
    await waitFor(() => expect(hook.current.isSubmitting(ronin)).toBe(false));

    // The shared mutation still reports every failure.
    expect(mocks.toastError).toHaveBeenCalledTimes(2);
  });

  it("clears a card once its request succeeds", async () => {
    mocks.create.mockResolvedValue({ id: "r1" });
    const heat = result(1, "Heat");
    const { result: hook } = renderHook(() => useSubmitMediaRequest(), { wrapper });

    act(() => hook.current.submit(heat));
    expect(hook.current.isSubmitting(heat)).toBe(true);

    await waitFor(() => expect(hook.current.isSubmitting(heat)).toBe(false));
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({ media_type: "movie", tmdb_id: 1, title: "Heat" }),
    );
    expect(mocks.toastSuccess).toHaveBeenCalledOnce();
  });
});
