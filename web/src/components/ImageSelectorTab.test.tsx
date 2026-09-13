import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useItemImages: vi.fn(),
  useApplyItemImage: vi.fn(),
}));

vi.mock("@/hooks/queries/items", () => ({
  useItemImages: (...args: unknown[]) => mocks.useItemImages(...args),
  useApplyItemImage: () => mocks.useApplyItemImage(),
}));

import ImageSelectorTab from "./ImageSelectorTab";

function item(type: ItemDetail["type"]): ItemDetail {
  return {
    content_id: `${type}-test`,
    type,
    title: "Test",
  } as ItemDetail;
}

describe("ImageSelectorTab", () => {
  it("tells season editors to update metadata plugins when galleries are incomplete", () => {
    mocks.useItemImages.mockReturnValue({
      data: { images: [], current: {} },
      isLoading: false,
      isError: false,
    });
    mocks.useApplyItemImage.mockReturnValue({ mutate: vi.fn(), isPending: false });

    render(<ImageSelectorTab item={item("season")} enabled />);

    expect(
      screen.getByText(/check for plugin updates and update TMDB and TVDB/i),
    ).toBeInTheDocument();
  });

  it("does not show the season plugin notice for series images", () => {
    mocks.useItemImages.mockReturnValue({
      data: { images: [], current: {} },
      isLoading: false,
      isError: false,
    });
    mocks.useApplyItemImage.mockReturnValue({ mutate: vi.fn(), isPending: false });

    render(<ImageSelectorTab item={item("series")} enabled />);

    expect(
      screen.queryByText(/check for plugin updates and update TMDB and TVDB/i),
    ).not.toBeInTheDocument();
  });
});
