import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { expect, it, vi } from "vitest";
const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: request,
}));
import { adminKeys } from "../keys";
import { useCreateLibrary, useUpdateLibrary } from "./libraries";

it.each([
  { trickplay_enabled: true },
  { trickplay_enabled: false },
  { enabled: true },
  { enabled: false },
])("refreshes preview status after explicit updates to %o", async (body) => {
  request.mockResolvedValue({ id: "1", name: "Fixture", type: "movies", paths: ["/fixture"] });
  const client = new QueryClient();
  const key = adminKeys.trickplayLibraries();
  client.setQueryData(key, []);
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const view = renderHook(() => useUpdateLibrary(), { wrapper });
  await act(() => view.result.current.mutateAsync({ id: 1, body }));
  expect(client.getQueryState(key)?.isInvalidated).toBe(true);
  view.unmount();
  client.clear();
});
it("refreshes preview status after creating an opted-in library", async () => {
  request.mockResolvedValue({ id: "1", name: "Fixture", type: "movies", paths: ["/fixture"] });
  const client = new QueryClient();
  const key = adminKeys.trickplayLibraries();
  client.setQueryData(key, []);
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const view = renderHook(() => useCreateLibrary(), { wrapper });
  await act(() =>
    view.result.current.mutateAsync({
      name: "Fixture",
      type: "movies",
      paths: ["/fixture"],
      trickplay_enabled: true,
    }),
  );
  expect(client.getQueryState(key)?.isInvalidated).toBe(true);
  view.unmount();
  client.clear();
});
