import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/hooks/queries/collectionPreviews", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/collectionPreviews")>(
    "@/hooks/queries/collectionPreviews",
  );

  return {
    ...actual,
    useAdminCollectionPreview: () => ({
      data: { items: [], total: 0 },
      isLoading: false,
      isFetching: false,
    }),
    useUserCollectionPreview: () => ({
      data: { items: [], total: 0 },
      isLoading: false,
      isFetching: false,
    }),
  };
});

import CollectionBuilder, {
  buildCollectionBuilderPreviewRequest,
  createCollectionBuilderValue,
} from "./CollectionBuilder";
import { getCollectionSortOptions } from "./collectionBuilderFields";

describe("CollectionBuilder", () => {
  it("renders guided mode by default for users", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <CollectionBuilder
          mode="user"
          value={createCollectionBuilderValue()}
          onChange={() => {}}
          onSubmit={() => {}}
        />
      </QueryClientProvider>,
    );

    expect(markup).toContain("Basics");
    expect(markup).toContain("Filters");
    expect(markup).toContain("Genres");
    expect(markup).toContain("Minimum IMDb Rating");
    expect(markup).not.toContain("Rule Groups");
  });

  it("renders advanced rule-groups mode when defaultAdvanced is set", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <CollectionBuilder
          mode="admin"
          value={createCollectionBuilderValue({ title: "Action Night" })}
          onChange={() => {}}
          onSubmit={() => {}}
          defaultAdvanced
        />
      </QueryClientProvider>,
    );

    expect(markup).toContain("Action Night");
    expect(markup).toContain("Rules");
    expect(markup).toContain("Ordering");
    expect(markup).not.toContain("Genres");
  });

  it("builds a preview request for smart collections", () => {
    const request = buildCollectionBuilderPreviewRequest(
      createCollectionBuilderValue({
        collection_type: "smart",
      }),
    );

    expect(request?.query_definition.sort.field).toBe("added_at");
    expect(request?.limit).toBe(12);
  });

  it("normalizes legacy rating aliases in smart collection preview requests", () => {
    const request = buildCollectionBuilderPreviewRequest(
      createCollectionBuilderValue({
        collection_type: "smart",
        query_definition: {
          library_ids: [],
          match: "all",
          groups: [{ match: "all", rules: [{ field: "rating", op: "gte", value: 8 }] }],
          sort: { field: "rating", order: "desc" },
        },
      }),
    );

    expect(request?.query_definition.groups[0]?.rules).toContainEqual({
      field: "rating_imdb",
      op: "gte",
      value: 8,
    });
    expect(request?.query_definition.sort.field).toBe("rating_imdb");
  });

  it("shows personalized sort options only on user collection surfaces", () => {
    expect(getCollectionSortOptions(false).map((sort) => sort.value)).not.toContain("progress");
    expect(getCollectionSortOptions(true).map((sort) => sort.value)).toEqual(
      expect.arrayContaining(["progress", "date_viewed", "plays"]),
    );
  });
});
