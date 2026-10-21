import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import type { RowCollections } from "@/lib/homeRows/types";
import { CollectionPicker } from "./CollectionPicker";

function option(
  id: string,
  title: string,
  collection_type: CollectionOption["collection_type"],
  source: CollectionOption["source"] = "library",
): CollectionOption {
  return { id, title, source, group: "Movies", collection_type, item_count: 23 };
}

const OPTIONS = [
  option("ghibli", "Studio Ghibli", "manual"),
  option("best", "Best Picture Winners", "mdblist"),
  option("xmas", "Christmas Classics", "smart"),
  option("tmdb", "TMDB Top", "tmdb", "user"),
  option("watchlist", "IMDb Watchlist", "mdblist", "user"),
  option("mine", "My Picks", "manual", "user"),
];

function collections(overrides: Partial<RowCollections> = {}): RowCollections {
  return {
    options: OPTIONS,
    loading: false,
    failed: false,
    href: "/admin/collections",
    ...overrides,
  };
}

function Harness({
  initial = "",
  source = collections(),
  onPick = vi.fn(),
}: {
  initial?: string;
  source?: RowCollections;
  onPick?: (option: CollectionOption) => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <CollectionPicker
      collections={source}
      value={value}
      onPageIds={new Set(["ghibli"])}
      pageLabel="Home"
      onPick={(picked) => {
        setValue(picked.id);
        onPick(picked);
      }}
    />
  );
}

function names() {
  return screen.getAllByRole("radio").map((radio) => radio.getAttribute("aria-label"));
}

describe("CollectionPicker", () => {
  it("filters by Manual, Smart and Synced list for library and personal collections alike", async () => {
    render(<Harness />);
    const filters = screen.getByRole("group", { name: "Collection type" });
    await userEvent.click(within(filters).getByRole("button", { name: "Manual" }));
    expect(within(filters).getByRole("button", { name: "Manual" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(names()).toEqual(["Studio Ghibli", "My Picks"]);
    await userEvent.click(within(filters).getByRole("button", { name: "Smart" }));
    expect(names()).toEqual(["Christmas Classics"]);
    await userEvent.click(within(filters).getByRole("button", { name: "Synced list" }));
    expect(names()).toEqual(["Best Picture Winners", "TMDB Top", "IMDb Watchlist"]);
    await userEvent.click(within(filters).getByRole("button", { name: "All" }));
    expect(names()).toHaveLength(6);
  });

  it("searches by name and says when nothing matches", async () => {
    render(<Harness />);
    await userEvent.type(screen.getByRole("searchbox", { name: "Search collections" }), "christ");
    expect(names()).toEqual(["Christmas Classics"]);
    await userEvent.clear(screen.getByRole("searchbox", { name: "Search collections" }));
    await userEvent.type(screen.getByRole("searchbox", { name: "Search collections" }), "zzz");
    expect(screen.queryAllByRole("radio")).toEqual([]);
    expect(screen.getByText('No collections match "zzz".')).toBeInTheDocument();
  });

  it("keeps a selection the filter hides and says the row keeps a collection that isn't listed", async () => {
    render(<Harness initial="gone" />);
    expect(
      screen.getByText(
        "This row's collection isn't in this list. The row keeps it until you pick another.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("radio", { checked: true })).toBeNull();
  });

  it("doesn't call a row's collection missing when the list didn't load", () => {
    render(<Harness initial="ghibli" source={collections({ options: [], failed: true })} />);
    expect(
      screen.getByText("Collections didn't load. Close this and try again."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/isn't in this list/)).toBeNull();
  });
});
