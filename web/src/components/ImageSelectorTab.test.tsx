import { fireEvent, render, screen } from "@testing-library/react";
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
  it.each([
    ["series", "poster"],
    ["series", "backdrop"],
    ["series", "logo"],
    ["season", "poster"],
  ] as const)("reports a successful %s %s selection to the metadata editor", (itemType, type) => {
    mocks.useItemImages.mockReturnValue({
      data: {
        images: [{ type, provider_id: "tmdb", original_url: `source-${type}`, url: `/${type}` }],
        current: {},
      },
      isLoading: false,
      isError: false,
    });
    const mutate = vi.fn((_request: unknown, options: { onSuccess: () => void }) =>
      options.onSuccess(),
    );
    mocks.useApplyItemImage.mockReturnValue({ mutate, isPending: false });
    const onImageApplied = vi.fn();

    render(<ImageSelectorTab item={item(itemType)} enabled onImageApplied={onImageApplied} />);
    const tab = { poster: "Posters", backdrop: "Backdrops", logo: "Logos" }[type];
    fireEvent.click(screen.getByRole("button", { name: tab }));
    fireEvent.click(screen.getByRole("button", { name: "TMDB" }));
    fireEvent.click(
      screen.getByRole("button", { name: `Apply ${type.charAt(0).toUpperCase()}${type.slice(1)}` }),
    );

    expect(mutate).toHaveBeenCalledWith(
      expect.objectContaining({ request: expect.objectContaining({ type }) }),
      expect.any(Object),
    );
    expect(onImageApplied).toHaveBeenCalledOnce();
  });

  it("reports an in-flight image apply to the metadata editor", () => {
    mocks.useItemImages.mockReturnValue({
      data: { images: [], current: {} },
      isLoading: false,
      isError: false,
    });
    mocks.useApplyItemImage.mockReturnValue({ mutate: vi.fn(), isPending: false });
    const onApplyPendingChange = vi.fn();
    const { rerender } = render(
      <ImageSelectorTab
        item={item("series")}
        enabled
        onApplyPendingChange={onApplyPendingChange}
      />,
    );
    mocks.useApplyItemImage.mockReturnValue({ mutate: vi.fn(), isPending: true });
    rerender(
      <ImageSelectorTab
        item={item("series")}
        enabled
        onApplyPendingChange={onApplyPendingChange}
      />,
    );

    expect(onApplyPendingChange).toHaveBeenCalledWith(true);
  });
});
