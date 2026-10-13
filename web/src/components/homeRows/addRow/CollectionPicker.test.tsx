import { useState } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
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
  option("trakt", "Trakt Watchlist", "trakt", "user"),
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
  locked,
}: {
  initial?: string;
  source?: RowCollections;
  onPick?: (option: CollectionOption) => void;
  locked?: boolean;
}) {
  const [value, setValue] = useState(initial);
  return (
    <CollectionPicker
      collections={source}
      value={value}
      onPageIds={new Set(["ghibli"])}
      pageLabel="Home"
      locked={locked}
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
  it("lists every collection as a radio with its kind and count, and marks ones on the page", () => {
    render(<Harness />);
    expect(screen.getByRole("radiogroup", { name: "Collection" })).toBeInTheDocument();
    expect(names()).toEqual([
      "Studio Ghibli",
      "Best Picture Winners",
      "Christmas Classics",
      "TMDB Top",
      "Trakt Watchlist",
      "My Picks",
    ]);
    const ghibli = screen.getByRole("radio", { name: "Studio Ghibli" });
    expect(ghibli).toHaveAccessibleDescription("Manual · 23 titles · On Home");
    expect(screen.getByRole("radio", { name: "Best Picture Winners" })).toHaveAccessibleDescription(
      "Synced list · 23 titles",
    );
    expect(screen.getByRole("searchbox", { name: "Search collections" })).toHaveAttribute(
      "placeholder",
      "Search 6 collections",
    );
  });

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
    expect(names()).toEqual(["Best Picture Winners", "TMDB Top", "Trakt Watchlist"]);
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

  it("picks with the keyboard and hands back the whole option", async () => {
    const onPick = vi.fn();
    render(<Harness onPick={onPick} />);
    await userEvent.click(screen.getByRole("radio", { name: "Studio Ghibli" }));
    expect(onPick).toHaveBeenLastCalledWith(OPTIONS[0]);
    // Held down: Radix checks a radio when an arrow key moves focus to it.
    await userEvent.keyboard("{ArrowDown>}");
    await waitFor(() =>
      expect(screen.getByRole("radio", { name: "Best Picture Winners" })).toBeChecked(),
    );
    await userEvent.keyboard("{/ArrowDown}");
    expect(onPick).toHaveBeenLastCalledWith(OPTIONS[1]);
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

  it("says when collections are loading, failed or there are none", () => {
    const { rerender } = render(<Harness source={collections({ options: [], loading: true })} />);
    expect(screen.getByText("Loading collections…")).toBeInTheDocument();
    rerender(<Harness source={collections({ options: [], failed: true })} />);
    expect(
      screen.getByText("Collections didn't load. Close this and try again."),
    ).toBeInTheDocument();
    rerender(<Harness source={collections({ options: [] })} />);
    expect(
      screen.getByText("No collections yet. Make one in Collections first."),
    ).toBeInTheDocument();
  });

  it("can't change a legacy Trakt row's collection", () => {
    render(<Harness initial="ghibli" locked />);
    for (const radio of screen.getAllByRole("radio")) expect(radio).toBeDisabled();
    expect(
      screen.getByText("This row follows a Trakt list, so its collection can't change."),
    ).toBeInTheDocument();
  });
});
