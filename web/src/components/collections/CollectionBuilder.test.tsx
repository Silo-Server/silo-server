import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { QueryDefinition } from "@/api/types";

vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogMetadataFilters: () => ({ data: undefined, isLoading: false }),
}));

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
  SmartCollectionLimitField,
} from "./CollectionBuilder";
import {
  COLLECTION_FIELD_OPTIONS,
  COLLECTION_SORT_OPTIONS,
  getCollectionSortOptions,
} from "./collectionBuilderFields";

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

  it("renders a dedicated preview sidebar layout when requested", () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <CollectionBuilder
          mode="admin"
          value={createCollectionBuilderValue()}
          onChange={() => {}}
          onSubmit={() => {}}
          previewLayout="sidebar"
          sidebarContent={<div>Collection Summary</div>}
        />
      </QueryClientProvider>,
    );

    expect(markup).toContain("Collection Summary");
    expect(markup).toContain("xl:grid-cols-[minmax(0,1fr)_22rem]");
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

  it("includes the expanded collection builder field and sort lists", () => {
    expect(COLLECTION_FIELD_OPTIONS.map((field) => field.value)).toEqual(
      expect.arrayContaining(["rating_imdb", "watched", "favorited", "in_watchlist"]),
    );
    expect(COLLECTION_SORT_OPTIONS.map((sort) => sort.value)).toContain("rating_imdb");
  });

  it("includes last_air_date in the sort options", () => {
    expect(COLLECTION_SORT_OPTIONS.map((sort) => sort.value)).toContain("last_air_date");
  });

  it("shows personalized sort options only on user collection surfaces", () => {
    expect(getCollectionSortOptions(false).map((sort) => sort.value)).not.toContain("progress");
    expect(getCollectionSortOptions(true).map((sort) => sort.value)).toEqual(
      expect.arrayContaining(["progress", "date_viewed", "plays"]),
    );
  });
});

describe("CollectionBuilder rules Guided can't show", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("opens them in Advanced and keeps Guided switched off", () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <CollectionBuilder
          mode="admin"
          value={createCollectionBuilderValue({
            query_definition: {
              match: "all",
              groups: [
                {
                  match: "any",
                  rules: [
                    { field: "genre", op: "is", value: "Comedy" },
                    { field: "genre", op: "is", value: "Drama" },
                  ],
                },
              ],
            },
          })}
          onChange={() => {}}
          onSubmit={() => {}}
          allowLibrarySelection={false}
        />
      </QueryClientProvider>,
    );

    expect(screen.getByText("Rule Groups")).toBeInTheDocument();
    expect(screen.queryByText("Genres")).toBeNull();
    expect(screen.getByRole("button", { name: "Switch to Guided" })).toBeDisabled();
    expect(screen.getByText("These rules use options the Guided view can't show.")).toBeVisible();
  });
});

describe("SmartCollectionLimitField", () => {
  afterEach(cleanup);

  function renderLimit(limit?: number) {
    const onQueryChange = vi.fn<(query: QueryDefinition) => void>();
    render(
      <SmartCollectionLimitField
        query={{ ...createCollectionBuilderValue().query_definition, limit }}
        onQueryChange={onQueryChange}
      />,
    );
    return { input: screen.getByLabelText("Max items"), onQueryChange };
  }

  it("shows a blank field and says blank means no limit", () => {
    const { input } = renderLimit();

    expect(input).toHaveValue(null);
    expect(screen.getByText("Leave blank for no limit.")).toBeVisible();
  });

  it("keeps a limit above 500", () => {
    const { input, onQueryChange } = renderLimit();

    fireEvent.change(input, { target: { value: "2000" } });
    fireEvent.blur(input);

    expect(onQueryChange).toHaveBeenCalledWith(expect.objectContaining({ limit: 2000 }));
  });

  it("removes the limit when the field is cleared", () => {
    const { input, onQueryChange } = renderLimit(250);
    expect(input).toHaveValue(250);

    fireEvent.change(input, { target: { value: "" } });
    fireEvent.blur(input);

    expect(onQueryChange).toHaveBeenCalledTimes(1);
    expect(onQueryChange.mock.calls[0]?.[0].limit).toBeUndefined();
  });
  it.each(["0", "-5"])("keeps the limit when %s is entered", (value) => {
    const { input, onQueryChange } = renderLimit(250);

    fireEvent.change(input, { target: { value } });
    fireEvent.blur(input);

    expect(onQueryChange).not.toHaveBeenCalled();
    expect(input).toHaveValue(250);
  });
});
