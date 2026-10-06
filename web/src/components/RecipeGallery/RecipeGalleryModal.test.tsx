import * as api from "@/lib/recipes";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import RecipeGalleryModal from "./RecipeGalleryModal";

vi.mock("@/lib/recipes");

beforeEach(() => {
  vi.mocked(api.fetchRecipeCatalog).mockResolvedValue({
    categories: {
      library_staples: [
        {
          type: "recently_added",
          category: "library_staples",
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
        },
      ],
      discovery: [
        {
          type: "hidden_gems",
          category: "discovery",
          avoid_duplicates: true,
          supports_rotation: false,
          admin_only: false,
          presets: [
            {
              key: "hg",
              display_name: "Hidden Gems",
              icon: "💎",
              description_short: "Underrated",
              default_params: {},
            },
          ],
        },
      ],
    },
  });
});

describe("RecipeGalleryModal", () => {
  it("filters by category chip", async () => {
    render(<RecipeGalleryModal open onClose={() => {}} onPick={() => {}} />);
    await waitFor(() => expect(screen.getByText("Recently Added")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("button", { name: /Discovery/i }));
    expect(screen.queryByText("Recently Added")).not.toBeInTheDocument();
    expect(screen.getByText("Hidden Gems")).toBeInTheDocument();
  });

  it("filters by search text", async () => {
    render(<RecipeGalleryModal open onClose={() => {}} onPick={() => {}} />);
    await waitFor(() => expect(screen.getByText("Recently Added")).toBeInTheDocument());

    await userEvent.type(screen.getByPlaceholderText(/search/i), "Hidden");
    expect(screen.queryByText("Recently Added")).not.toBeInTheDocument();
    expect(screen.getByText("Hidden Gems")).toBeInTheDocument();
  });

  it("invokes onPick when a card is clicked", async () => {
    const onPick = vi.fn();
    render(<RecipeGalleryModal open onClose={() => {}} onPick={onPick} />);
    await waitFor(() => expect(screen.getByText("Recently Added")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("button", { name: /Recently Added/ }));
    expect(onPick).toHaveBeenCalledWith(
      expect.objectContaining({ type: "recently_added" }),
      expect.objectContaining({ key: "ra" }),
    );
  });
});
