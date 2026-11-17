import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import { MAX_ROW_COLLECTIONS, type CollectionRef } from "@/lib/homeRows/collectionList";
import type { RowCollections } from "@/lib/homeRows/types";
import { CollectionsRowEditor } from "./CollectionsRowEditor";

vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p-other", name: "Alex" }] }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-me", name: "Sam" }, isLoading: false }),
}));

function option(
  id: string,
  title: string,
  source: CollectionOption["source"] = "library",
  creator?: string,
): CollectionOption {
  return {
    id,
    title,
    source,
    group: "Movies",
    collection_type: "manual",
    item_count: 12,
    ...(creator ? { creator_profile_id: creator } : {}),
  };
}

const OPTIONS = [
  option("marvel", "Marvel"),
  option("pixar", "Pixar"),
  option("ghibli", "Ghibli", "user", "p-me"),
  option("shared", "Alex's picks", "user", "p-other"),
];

function collections(options = OPTIONS): RowCollections {
  return { options, loading: false, failed: false, href: "/collections" };
}

function Harness({
  initial = [],
  source = collections(),
  onChange = vi.fn(),
}: {
  initial?: CollectionRef[];
  source?: RowCollections;
  onChange?: (refs: CollectionRef[]) => void;
}) {
  const [refs, setRefs] = useState(initial);
  return (
    <CollectionsRowEditor
      collections={source}
      refs={refs}
      onChange={(next) => {
        setRefs(next);
        onChange(next);
      }}
    />
  );
}

function chosenTitles() {
  const list = screen.getByRole("list", { name: "Collections in this row" });
  return within(list)
    .getAllByRole("listitem")
    .map((item) => item.querySelector(".font-semibold")?.textContent);
}

describe("CollectionsRowEditor", () => {
  it("adds collections in the order they are pressed and keeps the search", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    expect(screen.getByText("Add at least one collection below.")).toBeInTheDocument();

    const search = screen.getByRole("searchbox");
    await user.type(search, "i");
    await user.click(screen.getByRole("button", { name: "Pixar" }));
    await user.click(screen.getByRole("button", { name: "Ghibli" }));

    expect(search).toHaveValue("i");
    expect(screen.getByRole("button", { name: "Pixar" })).toHaveAttribute("aria-pressed", "true");
    expect(onChange).toHaveBeenLastCalledWith([
      { source: "library", id: "pixar" },
      { source: "user", id: "ghibli" },
    ]);
    expect(chosenTitles()).toEqual(["Pixar", "Ghibli"]);
    expect(screen.getByText("2 collections, shown in this order")).toBeInTheDocument();
  });

  it("moves and removes a collection with its own buttons", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <Harness
        onChange={onChange}
        initial={[
          { source: "library", id: "marvel" },
          { source: "library", id: "pixar" },
          { source: "user", id: "shared" },
        ]}
      />,
    );
    expect(screen.getAllByText(/by Alex/).length).toBeGreaterThan(0);

    await user.click(screen.getByRole("button", { name: "Move Pixar up" }));
    expect(chosenTitles()).toEqual(["Pixar", "Marvel", "Alex's picks"]);
    expect(screen.getByRole("button", { name: "Move Pixar up" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Remove Marvel" }));
    expect(onChange).toHaveBeenLastCalledWith([
      { source: "library", id: "pixar" },
      { source: "user", id: "shared" },
    ]);
  });

  it("keeps a collection it no longer offers, marked, until it is removed", async () => {
    const user = userEvent.setup();
    render(<Harness initial={[{ source: "library", id: "gone" }]} />);
    expect(screen.getByText("No longer available")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove No longer available" }));
    expect(screen.getByText("Add at least one collection below.")).toBeInTheDocument();
  });

  it("stops adding at the most a row holds, but still takes one out", async () => {
    const user = userEvent.setup();
    const many = Array.from({ length: MAX_ROW_COLLECTIONS + 1 }, (_, index) =>
      option(`c${index}`, `Collection ${index}`),
    );
    const full = many
      .slice(0, MAX_ROW_COLLECTIONS)
      .map((entry) => ({ source: entry.source, id: entry.id }));
    render(<Harness source={collections(many)} initial={full} />);

    expect(screen.getByText(`A row holds up to ${MAX_ROW_COLLECTIONS} collections.`)).toBeVisible();
    const last = screen.getByRole("button", { name: `Collection ${MAX_ROW_COLLECTIONS}` });
    expect(last).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Collection 0" }));
    expect(screen.getByRole("button", { name: `Collection ${MAX_ROW_COLLECTIONS}` })).toBeEnabled();
  });
});
