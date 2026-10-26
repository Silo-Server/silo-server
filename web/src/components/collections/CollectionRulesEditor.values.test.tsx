import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { QueryDefinition } from "@/api/types";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";

import CollectionRulesEditor from "./CollectionRulesEditor";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
installV2Recorder();

describe("CollectionRulesEditor value pickers", () => {
  it("look for values in the picked libraries and kind of titles", async () => {
    v2Recorder.answer("GET /api/v2/catalog/search/capabilities", {
      revision: "r1",
      state: "available",
      allowed: true,
      facet_value_search: true,
    });
    v2Recorder.answer("GET /api/v2/catalog/filters/search", {
      matches: ["Drama"],
      values: [{ value: "Drama", count: 12 }],
      has_more: false,
    });
    const value: QueryDefinition = {
      library_ids: [2, 1],
      media_scope: "movie",
      match: "all",
      groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "" }] }],
      sort: { field: "title", order: "asc" },
    };
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <CollectionRulesEditor
          value={value}
          onChange={() => {}}
          libraries={[
            { id: 1, name: "Movies" },
            { id: 2, name: "4K Movies" },
          ]}
        />
      </QueryClientProvider>,
    );

    await userEvent.click(screen.getByRole("combobox", { name: "Value" }));
    expect(await screen.findByRole("option", { name: "Drama, 12 titles" })).toBeInTheDocument();
    await waitFor(() =>
      expect(v2Recorder.callsOf("GET /api/v2/catalog/filters/search").map((c) => c.query)).toEqual([
        {
          source: "query",
          facet: "genre",
          q: "",
          limit: 50,
          type: "movie",
          library_ids: ["1", "2"],
        },
      ]),
    );
  });
});
