// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { adminKeys } from "@/hooks/queries/keys";
import {
  useCatalogImportSources,
  useLocalImportSources,
  useFilesystemBrowseWhen,
  fetchFilesystemBrowse,
} from "./libraries";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
afterEach(() => vi.unstubAllGlobals());
function setup(handler: (url: URL) => Response) {
  const calls: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      calls.push(url);
      return handler(url);
    }),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    calls,
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
it.each([false, true])("loads source pages only on request, local=%s", async (local) => {
  const { calls, wrapper } = setup((url) =>
    jsonResponse(
      url.searchParams.has("cursor")
        ? { items: [{ key: "seed.json.gz", size_bytes: 1 }], page: { has_more: false } }
        : { items: [], page: { has_more: true, next_cursor: "opaque" } },
    ),
  );
  const { result } = renderHook(
    () => (local ? useLocalImportSources() : useCatalogImportSources()),
    { wrapper },
  );
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toEqual([]);
  expect(result.current.hasNextPage).toBe(true);
  expect(calls).toHaveLength(1);
  expect(calls[0]?.pathname).toBe(
    `/api/v2/admin/catalog/${local ? "local-import-sources" : "import-sources"}`,
  );
  await act(() => result.current.fetchNextPage());
  expect(calls[1]?.searchParams.get("cursor")).toBe("opaque");
  await waitFor(() => expect(result.current.data?.map((s) => s.key)).toEqual(["seed.json.gz"]));
  expect(result.current.hasNextPage).toBe(false);
});
it("keeps path validation separate from paged browsing and filters autocomplete on the server", async () => {
  const { calls, client, wrapper } = setup(() =>
    jsonResponse({
      path: "/media",
      parent: "/",
      items: [{ path: "/media/zebra", name: "zebra" }],
      page: { has_more: false },
    }),
  );
  await client.fetchQuery({
    queryKey: adminKeys.filesystemBrowse("/media"),
    queryFn: () => fetchFilesystemBrowse("/media"),
  });
  const { result, rerender } = renderHook(
    ({ prefix }) => useFilesystemBrowseWhen("/media", true, prefix),
    { wrapper, initialProps: { prefix: "ze" } },
  );
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(calls).toHaveLength(2);
  expect(calls[0]?.searchParams.get("limit")).toBe("1");
  expect(calls[1]?.searchParams.get("name_prefix")).toBe("ze");
  expect(calls[1]?.searchParams.get("limit")).toBe("50");
  rerender({ prefix: "ya" });
  await waitFor(() => expect(calls).toHaveLength(3));
  expect(calls[2]?.searchParams.get("name_prefix")).toBe("ya");
});
