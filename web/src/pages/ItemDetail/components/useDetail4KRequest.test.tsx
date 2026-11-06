import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RequestMediaDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  detail: vi.fn(),
  create: vi.fn(),
  creating: false,
  own: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: () => ({ data: mocks.status() }),
  useRequestMediaDetail: (...args: unknown[]) => ({ data: mocks.detail(...args) }),
  useCreateMediaRequest: () => ({ mutate: mocks.create, isPending: mocks.creating }),
  useCancelMediaRequest: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p" } }),
}));
vi.mock("./useOwnCancellableRequest", () => ({ useOwnCancellableRequest: () => mocks.own() }));

import { useDetail4KRequest } from "./useDetail4KRequest";

const heat = {
  media_type: "movie",
  tmdb_id: 949,
  title: "Heat",
  availability: "available",
  request: { requestable: false, reason: "already_available" },
  request_4k: { requestable: true },
} as RequestMediaDetail;

describe("useDetail4KRequest", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.creating = false;
    mocks.detail.mockReturnValue(heat);
    mocks.own.mockReturnValue(undefined);
  });

  it("reads nothing and offers nothing while the viewer may not request 4K", () => {
    mocks.status.mockReturnValue({ requests_enabled: true, requests_4k_allowed: false });
    const { result } = renderHook(() => useDetail4KRequest("movie", 949, "Heat"));
    expect(mocks.detail).toHaveBeenCalledWith("movie", 949, { enabled: false });
    expect(result.current.onRequest4K).toBeUndefined();
    expect(result.current.onCancel4KRequest).toBeUndefined();
  });

  it("requests a movie in the library in 4K", () => {
    mocks.status.mockReturnValue({ requests_enabled: true, requests_4k_allowed: true });
    const { result } = renderHook(() => useDetail4KRequest("movie", 949, "Heat"));
    result.current.onRequest4K?.();
    expect(mocks.create.mock.calls[0]![0]).toMatchObject({ tmdb_id: 949, is_4k: true });
  });

  it("offers no second 4K request while the first is sending", () => {
    mocks.status.mockReturnValue({ requests_enabled: true, requests_4k_allowed: true });
    mocks.creating = true;
    const { result } = renderHook(() => useDetail4KRequest("movie", 949, "Heat"));
    expect(result.current.onRequest4K).toBeUndefined();
  });

  it("offers to cancel the viewer's open 4K request instead of another", () => {
    mocks.status.mockReturnValue({ requests_enabled: true, requests_4k_allowed: true });
    mocks.detail.mockReturnValue({
      ...heat,
      request_4k: { requestable: false, reason: "already_requested", status: "pending" },
    });
    mocks.own.mockReturnValue({ id: "r-4k", is_4k: true });
    const { result } = renderHook(() => useDetail4KRequest("movie", 949, "Heat"));
    expect(result.current.onRequest4K).toBeUndefined();
    expect(result.current.onCancel4KRequest).toBeDefined();
  });
});
