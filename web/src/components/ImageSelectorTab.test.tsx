import { fireEvent, render, screen, within } from "@testing-library/react";
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
    const notice = screen.queryByText(/check for plugin updates and update TMDB and TVDB/i);
    if (itemType === "season") expect(notice).toBeInTheDocument();
    else expect(notice).not.toBeInTheDocument();
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

  it("filters choices by provider, with counts that follow Textless", () => {
    const poster = (provider_id: string, original_url: string, language = "") => ({
      type: "poster",
      provider_id,
      original_url,
      url: `/${original_url}`,
      language,
    });
    mocks.useItemImages.mockReturnValue({
      data: {
        images: [
          poster("local", "file:///poster.jpg"),
          poster("tmdb", "tmdb-1"),
          poster("tmdb", "tmdb-2", "en"),
          { ...poster("tvdb", "tvdb-1"), type: "backdrop" },
        ],
        current: {},
      },
      isLoading: false,
      isError: false,
    });
    mocks.useApplyItemImage.mockReturnValue({ mutate: vi.fn(), isPending: false });

    render(<ImageSelectorTab item={item("movie")} enabled />);
    const chips = within(screen.getByRole("group", { name: "Filter by provider" }));
    expect(chips.getAllByRole("button").map((chip) => chip.textContent)).toEqual([
      "All3",
      "LOCAL1",
      "TMDB2",
    ]);

    fireEvent.click(chips.getByRole("button", { name: /^TMDB\s*2$/ }));
    expect(screen.queryAllByRole("button", { name: "LOCAL" })).toHaveLength(0);
    expect(screen.getAllByRole("button", { name: "TMDB" })).toHaveLength(2);

    fireEvent.click(screen.getByRole("button", { name: "Textless" }));
    expect(chips.getByRole("button", { name: /^TMDB\s*1$/ })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "TMDB" })).toHaveLength(1);
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
