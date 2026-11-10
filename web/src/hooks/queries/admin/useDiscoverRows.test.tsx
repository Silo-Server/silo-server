import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { DiscoverLayout } from "@/api/v2/adminRequests";
import { V2ProblemError } from "@/api/v2/request";

const api = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }));

vi.mock("@/api/v2/adminRequests", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/adminRequests")>("@/api/v2/adminRequests")),
  getAdminDiscoverLayoutV2: () => api.get(),
  putAdminDiscoverLayoutV2: (...args: unknown[]) => api.put(...args),
}));

import { RowChangedError } from "@/lib/homeRows/types";
import { RowRefusedError, useDiscoverRows } from "./useDiscoverRows";

const layout: DiscoverLayout = {
  customized: true,
  max_rows: 30,
  updated_at: "",
  etag: '"r1"',
  rows: [
    {
      id: "trending_movies",
      type: "tmdb_chart",
      title: "Trending Movies",
      enabled: true,
      limit: 20,
      chart: { preset: "trending", media_type: "movie", time_window: "week" },
    },
    { id: "genres", type: "genres", title: "Genres", enabled: true },
  ],
};

function problem(
  status: number,
  type: string,
  errors?: Array<{ location: string; detail: string }>,
) {
  return new V2ProblemError("updateRequestDiscoverLayout", {
    type: `https://silo.example/problems/${type}`,
    title: type,
    status,
    detail: type,
    instance: "/api/v2/admin/request-discover/layout",
    ...(errors ? { errors: errors.map((error) => ({ ...error, code: "invalid" })) } : {}),
  });
}

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return renderHook(() => useDiscoverRows(), { wrapper });
}

describe("useDiscoverRows", () => {
  beforeEach(() => {
    api.get.mockReset().mockResolvedValue(layout);
    api.put.mockReset();
  });

  it("shows the page's conflict and the stored rows when another admin changed the layout", async () => {
    api.put.mockRejectedValue(problem(412, "precondition_failed"));
    const { result } = mount();
    await waitFor(() => expect(result.current.status).toBe("ready"));

    await act(() => result.current.setShown("genres", false));

    expect(api.put).toHaveBeenCalledWith(
      [
        expect.objectContaining({ id: "trending_movies" }),
        expect.objectContaining({ id: "genres", enabled: false }),
      ],
      { etag: '"r1"' },
    );
    expect(result.current.conflict).toEqual({ scope: "page" });
    expect(result.current.rows.find((row) => row.id === "genres")?.shown).toBe(true);
  });

  it("adds a row at the end and resolves with the id the server gave it", async () => {
    const added = {
      type: "mdblist" as const,
      title: "Anime",
      enabled: true,
      list: { url: "https://mdblist.com/lists/a/b" },
    };
    api.put.mockResolvedValue({
      ...layout,
      etag: '"r2"',
      rows: [...layout.rows, { ...added, id: "r_new" }],
    });
    const { result } = mount();
    await waitFor(() => expect(result.current.status).toBe("ready"));

    let id: string | undefined;
    await act(async () => {
      id = await result.current.saveRow(added);
    });

    expect(id).toBe("r_new");
    expect(api.put.mock.calls[0]![0]).toHaveLength(3);
  });

  it("refuses to save an edit over a row another admin changed since it opened", async () => {
    const { result } = mount();
    await waitFor(() => expect(result.current.status).toBe("ready"));
    const opened = { ...layout.rows[1]!, title: "Genres (old)" };

    await expect(
      result.current.saveRow({ ...opened, title: "Browse" }, opened),
    ).rejects.toBeInstanceOf(RowChangedError);
    expect(api.put).not.toHaveBeenCalled();
  });

  it("words the server's refusal of the saved row, and only that row", async () => {
    api.put.mockRejectedValue(
      problem(422, "validation_failed", [
        { location: "body.rows[2].list.url", detail: "must be an mdblist.com list" },
        { location: "body.rows[0].title", detail: "a row needs a title" },
      ]),
    );
    const { result } = mount();
    await waitFor(() => expect(result.current.status).toBe("ready"));

    const saving = result.current.saveRow({
      type: "mdblist",
      title: "Anime",
      enabled: true,
      list: { url: "https://example.com" },
    });

    await expect(saving).rejects.toEqual(
      new RowRefusedError(["list.url: must be an mdblist.com list"]),
    );
  });
});
