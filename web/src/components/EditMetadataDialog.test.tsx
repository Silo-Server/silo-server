import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  update: vi.fn(),
  updateAsync: vi.fn(),
  refresh: vi.fn(),
}));

vi.mock("@/hooks/queries/items", () => ({
  useUpdateItemMetadata: () => ({
    mutate: mocks.update,
    mutateAsync: mocks.updateAsync,
    isPending: false,
  }),
  useRefreshItemMetadata: () => ({ mutate: mocks.refresh }),
}));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => true }));
vi.mock("@/components/MetadataTranslatePanel", () => ({ MetadataTranslatePanel: () => null }));
vi.mock("@/components/ConfirmDialog", () => ({
  ConfirmDialog: ({
    open,
    confirmLabel,
    onConfirm,
  }: {
    open: boolean;
    confirmLabel: string;
    onConfirm: () => void;
  }) => (open ? <button onClick={onConfirm}>{confirmLabel}</button> : null),
}));
vi.mock("@/components/ImageSelectorTab", () => ({
  default: ({
    onImageApplied,
    onApplyPendingChange,
  }: {
    onImageApplied?: () => void;
    onApplyPendingChange?: (pending: boolean) => void;
  }) => (
    <>
      <button onClick={onImageApplied}>Apply image</button>
      <button onClick={() => onApplyPendingChange?.(true)}>Begin image apply</button>
      <button onClick={() => onApplyPendingChange?.(false)}>Finish image apply</button>
    </>
  ),
}));

import EditMetadataDialog from "./EditMetadataDialog";

function series(lockedFields: number[] = []): ItemDetail {
  return {
    content_id: "series-test",
    type: "series",
    title: "Test Series",
    locked_fields: lockedFields,
  } as ItemDetail;
}

describe("EditMetadataDialog image locks", () => {
  beforeEach(() => {
    mocks.update.mockReset();
    mocks.refresh.mockReset();
  });

  it("keeps a newly fetched image lock when other metadata is saved", () => {
    const onOpenChange = vi.fn();
    const { rerender } = render(
      <EditMetadataDialog item={series()} open onOpenChange={onOpenChange} />,
    );
    rerender(<EditMetadataDialog item={series([10])} open onOpenChange={onOpenChange} />);

    fireEvent.change(screen.getByDisplayValue("Test Series"), {
      target: { value: "Updated Series" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith(
      { title: "Updated Series", locked_fields: [0, 10] },
      expect.any(Object),
    );
  });

  it("includes the image lock when saving before the item has refetched", () => {
    render(<EditMetadataDialog item={series()} open onOpenChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Images" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply image" }));
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith({ locked_fields: [10] }, expect.any(Object));
  });

  it("waits for an image apply to finish before saving metadata", () => {
    render(<EditMetadataDialog item={series()} open onOpenChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Begin image apply" }));
    expect(screen.getByRole("button", { name: "Save Changes" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Finish image apply" }));
    expect(screen.getByRole("button", { name: "Save Changes" })).toBeEnabled();
  });
});

describe("EditMetadataDialog field locks", () => {
  beforeEach(() => {
    mocks.update.mockReset();
    mocks.updateAsync.mockReset();
    mocks.refresh.mockReset();
  });

  it("locks the overview when only the tagline changes", () => {
    render(<EditMetadataDialog item={series()} open onOpenChange={vi.fn()} />);
    fireEvent.change(screen.getByPlaceholderText("No tagline"), {
      target: { value: "Edited tagline" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith(
      { tagline: "Edited tagline", locked_fields: [1] },
      expect.any(Object),
    );
  });

  // The server clears the locks with the reset's own write, so a reset that
  // fails leaves them in place (#3581).
  it("resets on the server without unlocking first", () => {
    const onOpenChange = vi.fn();
    const item = series([0, 1, 10]);
    render(<EditMetadataDialog item={item} open onOpenChange={onOpenChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Reset to Provider" }));
    fireEvent.click(screen.getByText("Reset & Refresh"));

    expect(mocks.refresh).toHaveBeenCalledWith({ item, mode: "reset" });
    expect(mocks.updateAsync).not.toHaveBeenCalled();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});

describe("EditMetadataDialog season and episode locks", () => {
  beforeEach(() => {
    mocks.update.mockReset();
    mocks.updateAsync.mockReset();
    mocks.refresh.mockReset();
  });

  function child(type: "season" | "episode", lockedFields: number[] = []): ItemDetail {
    return {
      content_id: `${type}-test`,
      type,
      title: "Provider title",
      overview: "Provider overview",
      air_date: "2020-05-06",
      locked_fields: lockedFields,
    } as ItemDetail;
  }

  it.each(["season", "episode"] as const)("locks an edited %s's fields", (type) => {
    render(<EditMetadataDialog item={child(type)} open onOpenChange={vi.fn()} />);
    fireEvent.change(screen.getByDisplayValue("Provider title"), {
      target: { value: "Edited title" },
    });
    fireEvent.change(screen.getByDisplayValue("Provider overview"), {
      target: { value: "Edited overview" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Dates & Ratings" }));
    fireEvent.change(screen.getByDisplayValue("2020-05-06"), {
      target: { value: "2001-02-03" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith(
      {
        title: "Edited title",
        overview: "Edited overview",
        air_date: "2001-02-03",
        locked_fields: [0, 1, 13],
      },
      expect.any(Object),
    );
  });

  it("locks an edited episode's runtime", () => {
    render(
      <EditMetadataDialog
        item={{ ...child("episode"), runtime: 53 }}
        open
        onOpenChange={vi.fn()}
      />,
    );
    fireEvent.change(screen.getByDisplayValue("53"), { target: { value: "77" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith(
      { runtime: 77, locked_fields: [7] },
      expect.any(Object),
    );
  });

  // These rows don't store a tagline or a sort title, so the edit is dropped and
  // must not freeze the provider's overview or title.
  it("doesn't lock a field for an edit the row can't store", () => {
    render(<EditMetadataDialog item={child("episode")} open onOpenChange={vi.fn()} />);
    fireEvent.change(screen.getByPlaceholderText("No tagline"), {
      target: { value: "Edited tagline" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith({ tagline: "Edited tagline" }, expect.any(Object));
  });

  it("leaves a season image's lock on the series", () => {
    const onOpenChange = vi.fn();
    render(<EditMetadataDialog item={child("season")} open onOpenChange={onOpenChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Images" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply image" }));
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).not.toHaveBeenCalled();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("shows the locked count and resets on the server", () => {
    const item = child("episode", [0, 13]);
    render(<EditMetadataDialog item={item} open onOpenChange={vi.fn()} />);
    expect(screen.getByText("2 locked")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Reset to Provider" }));
    fireEvent.click(screen.getByText("Reset & Refresh"));

    expect(mocks.refresh).toHaveBeenCalledWith({ item, mode: "reset" });
    expect(mocks.updateAsync).not.toHaveBeenCalled();
  });
});

describe("EditMetadataDialog cleared fields", () => {
  beforeEach(() => {
    mocks.update.mockReset();
  });

  // The server clears a nullable field only on an explicit null; it leaves an
  // omitted field as it was.
  it("sends null for a cleared rating, date, and air timezone", () => {
    const item = {
      ...series(),
      rating_imdb: 7.2,
      first_air_date: "2016-03-08",
      air_timezone: "America/New_York",
    } as ItemDetail;
    render(<EditMetadataDialog item={item} open onOpenChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Dates & Ratings" }));
    for (const value of ["7.2", "2016-03-08", "America/New_York"]) {
      fireEvent.change(screen.getByDisplayValue(value), { target: { value: "" } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith(
      expect.objectContaining({ rating_imdb: null, first_air_date: null, air_timezone: null }),
      expect.any(Object),
    );
  });
});

describe("EditMetadataDialog audiobooks", () => {
  beforeEach(() => {
    mocks.update.mockReset();
  });

  it("edits and locks an audiobook's title and year", () => {
    const book = {
      content_id: "book-test",
      type: "audiobook",
      title: "Test Book",
      year: 2019,
      locked_fields: [],
    } as unknown as ItemDetail;
    render(<EditMetadataDialog item={book} open onOpenChange={vi.fn()} />);

    // Video provider IDs don't apply to a book; Match Item sets its provider IDs.
    expect(screen.queryByRole("button", { name: "External IDs" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Images" })).toBeInTheDocument();

    fireEvent.change(screen.getByDisplayValue("Test Book"), { target: { value: "Edited Book" } });
    fireEvent.click(screen.getByRole("button", { name: "Dates & Ratings" }));
    fireEvent.change(screen.getByDisplayValue("2019"), { target: { value: "2001" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith(
      { title: "Edited Book", year: 2001, locked_fields: [0, 13] },
      expect.any(Object),
    );
  });
});
