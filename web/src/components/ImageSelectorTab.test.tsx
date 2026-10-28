import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useItemImages: vi.fn(),
  useApplyItemImage: vi.fn(),
  useUploadItemImage: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/hooks/queries/items", () => ({
  useItemImages: (...args: unknown[]) => mocks.useItemImages(...args),
  useApplyItemImage: () => mocks.useApplyItemImage(),
  useUploadItemImage: () => mocks.useUploadItemImage(),
}));

vi.mock("sonner", () => ({ toast: { error: mocks.toastError } }));

import ImageSelectorTab from "./ImageSelectorTab";

function item(type: ItemDetail["type"]): ItemDetail {
  return {
    content_id: `${type}-test`,
    type,
    title: "Test",
  } as ItemDetail;
}

function chooseFile(file: File) {
  fireEvent.change(screen.getByTestId("image-upload-input"), { target: { files: [file] } });
}

function noImages() {
  mocks.useItemImages.mockReturnValue({
    data: { images: [], current: {} },
    isLoading: false,
    isError: false,
  });
}

describe("ImageSelectorTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.useApplyItemImage.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useUploadItemImage.mockReturnValue({ mutate: vi.fn(), isPending: false });
  });

  it.each([
    ["series", "poster"],
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

  it("uploads a chosen file for the active tab and reports it to the metadata editor", () => {
    noImages();
    const mutate = vi.fn((_request: unknown, options: { onSuccess: () => void }) =>
      options.onSuccess(),
    );
    mocks.useUploadItemImage.mockReturnValue({ mutate, isPending: false });
    const onImageApplied = vi.fn();
    const series = item("series");

    render(<ImageSelectorTab item={series} enabled onImageApplied={onImageApplied} />);
    fireEvent.click(screen.getByRole("button", { name: "Logos" }));
    expect(screen.getByRole("button", { name: "Upload logo" })).toBeEnabled();
    const file = new File(["logo"], "logo.png", { type: "image/png" });
    chooseFile(file);

    expect(mutate).toHaveBeenCalledWith({ item: series, type: "logo", file }, expect.any(Object));
    expect(onImageApplied).toHaveBeenCalledOnce();
  });

  it("uploads a file whose type the browser cannot name, for the server to check", () => {
    noImages();
    const mutate = vi.fn();
    mocks.useUploadItemImage.mockReturnValue({ mutate, isPending: false });

    render(<ImageSelectorTab item={item("movie")} enabled />);
    const file = new File(["jpeg"], "poster", { type: "" });
    chooseFile(file);

    expect(mutate).toHaveBeenCalledWith(expect.objectContaining({ file }), expect.any(Object));
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it.each([
    ["a format the server does not take", new File(["gif"], "poster.gif", { type: "image/gif" })],
    [
      "a file over 10 MiB",
      new File([new Uint8Array(10 * 1024 * 1024 + 1)], "poster.png", { type: "image/png" }),
    ],
  ])("refuses %s before uploading", (_name, file) => {
    noImages();
    const mutate = vi.fn();
    mocks.useUploadItemImage.mockReturnValue({ mutate, isPending: false });

    render(<ImageSelectorTab item={item("movie")} enabled />);
    chooseFile(file);

    expect(mutate).not.toHaveBeenCalled();
    expect(mocks.toastError).toHaveBeenCalledOnce();
  });

  it("marks the uploaded image the item shows as current, under Local", () => {
    mocks.useItemImages.mockReturnValue({
      data: {
        images: [
          {
            type: "poster",
            provider_id: "local",
            original_url: "upload://sha256:abc",
            url: "/uploaded",
            language: "",
          },
          {
            type: "poster",
            provider_id: "tmdb",
            original_url: "tmdb-1",
            url: "/tmdb",
            language: "",
          },
        ],
        current: { poster_url: "local/movies/movie-test/abc/poster/original.webp" },
      },
      isLoading: false,
      isError: false,
    });

    render(<ImageSelectorTab item={item("movie")} enabled />);

    // Tiles are named by their badges: the upload's carries Current.
    expect(screen.getByRole("button", { name: /^Current\s*LOCAL$/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "TMDB" })).toBeInTheDocument();
  });

  it("reports an in-flight upload to the metadata editor and blocks another", () => {
    noImages();
    mocks.useUploadItemImage.mockReturnValue({ mutate: vi.fn(), isPending: true });
    const onApplyPendingChange = vi.fn();

    render(
      <ImageSelectorTab
        item={item("season")}
        enabled
        onApplyPendingChange={onApplyPendingChange}
      />,
    );

    expect(onApplyPendingChange).toHaveBeenCalledWith(true);
    expect(screen.getByRole("button", { name: "Upload poster" })).toBeDisabled();
    expect(screen.getByText("Uploading...")).toBeInTheDocument();
  });
});
