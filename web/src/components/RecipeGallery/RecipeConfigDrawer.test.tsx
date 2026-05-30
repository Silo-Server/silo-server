import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import RecipeConfigDrawer from "./RecipeConfigDrawer";

vi.mock("@/api/client", () => ({
  api: vi.fn(async (path: string) => {
    if (path === "/libraries") {
      return [
        { id: 1, name: "Movies" },
        { id: 2, name: "Shows" },
      ];
    }
    return {};
  }),
}));

vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({ collections: [], isLoading: false }),
}));

const def = {
  type: "recently_added",
  category: "library_staples" as const,
  avoid_duplicates: false,
  supports_rotation: false,
  admin_only: false,
  presets: [
    {
      key: "ra",
      display_name: "Recently Added",
      icon: "🆕",
      description_short: "Latest",
      default_params: {},
    },
  ],
};
const preset = def.presets[0]!;

describe("RecipeConfigDrawer", () => {
  it("renders title prefilled with preset display_name", () => {
    render(<RecipeConfigDrawer def={def} preset={preset} onCancel={() => {}} onAdd={() => {}} />);
    const title = screen.getByLabelText(/title/i) as HTMLInputElement;
    expect(title.value).toBe("Recently Added");
  });

  it("calls onAdd with title, params, and limit", async () => {
    const onAdd = vi.fn();
    render(<RecipeConfigDrawer def={def} preset={preset} onCancel={() => {}} onAdd={onAdd} />);
    await userEvent.click(screen.getByRole("button", { name: /add section/i }));
    expect(onAdd).toHaveBeenCalledWith(
      expect.objectContaining({ title: "Recently Added", item_limit: 20, config: {} }),
    );
  });

  it("requires a collection before submitting collection presets", async () => {
    const collectionDef = {
      ...def,
      type: "collection",
      presets: [
        {
          key: "trakt_recommended_shows",
          display_name: "Trakt Recommended Shows",
          icon: "🎯",
          description_short: "Recommended shows",
          default_params: {
            library_collection_id: "",
            source_provider: "trakt",
            source_preset: "recommended",
            media_type: "tv",
          },
        },
      ],
    };
    const onAdd = vi.fn();

    render(
      <RecipeConfigDrawer
        def={collectionDef}
        preset={collectionDef.presets[0]!}
        onCancel={() => {}}
        onAdd={onAdd}
      />,
    );

    const addButton = screen.getByRole("button", { name: /add section/i });
    expect(addButton).toBeDisabled();
    expect(screen.getByText(/choose a synced collection/i)).toBeInTheDocument();
    expect(screen.getByText(/no synced trakt recommended shows collection/i)).toBeInTheDocument();

    await userEvent.click(addButton);
    expect(onAdd).not.toHaveBeenCalled();
  });

  it("hides the collection picker for auto-backed Trakt public presets", async () => {
    const collectionDef = {
      ...def,
      type: "collection",
      presets: [
        {
          key: "trakt_trending_movies",
          display_name: "Trakt Trending Movies",
          icon: "📈",
          description_short: "Trending movies",
          default_params: {
            library_collection_id: "",
            source_provider: "trakt",
            source_preset: "trending",
            media_type: "movie",
          },
        },
      ],
    };
    const onAdd = vi.fn();

    render(
      <RecipeConfigDrawer
        def={collectionDef}
        preset={collectionDef.presets[0]!}
        onCancel={() => {}}
        onAdd={onAdd}
      />,
    );

    expect(screen.queryByText(/^Collection$/i)).not.toBeInTheDocument();
    expect(screen.getByText(/will be created automatically/i)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /add section/i }));
    expect(onAdd).toHaveBeenCalledWith(
      expect.objectContaining({
        title: "Trakt Trending Movies",
        config: expect.objectContaining({
          source_provider: "trakt",
          source_preset: "trending",
          media_type: "movie",
        }),
      }),
    );
  });

  it("delegates manually selected bulk libraries to onAdd", async () => {
    const onAdd = vi.fn().mockResolvedValue(undefined);
    render(<RecipeConfigDrawer def={def} preset={preset} onCancel={() => {}} onAdd={onAdd} />);

    await userEvent.click(screen.getByLabelText(/apply to all libraries/i));
    await userEvent.click(screen.getByRole("button", { name: /add section/i }));

    await screen.findByLabelText("Movies");
    await userEvent.click(screen.getByLabelText("Shows"));
    await userEvent.click(screen.getByRole("button", { name: /apply \(1\)/i }));

    await waitFor(() => {
      expect(onAdd).toHaveBeenCalledWith(
        expect.objectContaining({
          apply_to_all_libraries: true,
          library_ids: [1],
          section_type: "recently_added",
          title: "Recently Added",
        }),
      );
    });
  });
});
