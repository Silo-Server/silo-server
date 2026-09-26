// @vitest-environment jsdom

import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { adminKeys } from "../keys";

const mocks = vi.hoisted(() => ({ v2: vi.fn() }));

vi.mock("@/api/v2/request", () => ({
  v2: mocks.v2,
  V2ProblemError: class V2ProblemError extends Error {},
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { useCreateStorageTransition } from "./settings";

beforeEach(() => mocks.v2.mockReset());
afterEach(cleanup);

it("invalidates paginated storage-transition jobs after admission", async () => {
  mocks.v2.mockResolvedValue({ job: { id: "transition-job" } });
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const invalidate = vi.spyOn(client, "invalidateQueries");
  const { result } = renderHook(() => useCreateStorageTransition(), { wrapper });

  act(() => {
    result.current.mutate({ policy: "start_fresh", values: {} });
  });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));

  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["admin", "jobs"] });
  expect(invalidate).toHaveBeenCalledWith({
    queryKey: adminKeys.jobs("storage_transition"),
  });
});
