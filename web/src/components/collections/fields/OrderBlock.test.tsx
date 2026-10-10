import { fireEvent, render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, it, vi } from "vitest";

import type { QueryDefinition } from "@/api/types";

import { OrderBlock } from "./OrderBlock";

vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

beforeAll(() => {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});

const rules: QueryDefinition = {
  library_ids: [1],
  match: "all",
  groups: [],
  sort: { field: "added_at", order: "desc" },
};

function renderSmart({
  limit,
  sortConfig = {},
  allowPersonalized = false,
}: {
  limit?: number;
  sortConfig?: Record<string, unknown>;
  allowPersonalized?: boolean;
} = {}) {
  const onChange =
    vi.fn<(next: { rules: QueryDefinition; sortConfig?: Record<string, unknown> }) => void>();
  render(
    <OrderBlock
      mode="smart"
      rules={{ ...rules, limit }}
      sortConfig={sortConfig}
      allowPersonalized={allowPersonalized}
      onChange={onChange}
    />,
  );
  return { input: screen.getByRole("spinbutton", { name: "Max titles" }), onChange };
}

function choose(combobox: HTMLElement, option: string) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  fireEvent.click(screen.getByRole("option", { name: option }));
}

describe("OrderBlock, Smart", () => {
  it("shows no limit as a blank field", () => {
    const { input } = renderSmart();
    expect(input).toHaveValue(null);
    expect(input).toHaveAttribute("placeholder", "No limit");
  });

  it("commits a whole number on Enter", () => {
    const { input, onChange } = renderSmart();
    input.focus();
    fireEvent.change(input, { target: { value: "40" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(input).not.toHaveFocus();
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith({ rules: expect.objectContaining({ limit: 40 }) });
  });

  it("keeps a limit above 500", () => {
    const { input, onChange } = renderSmart();
    fireEvent.change(input, { target: { value: "2000" } });
    fireEvent.blur(input);
    expect(onChange).toHaveBeenCalledWith({ rules: expect.objectContaining({ limit: 2000 }) });
  });

  it("removes the limit when the field is cleared", () => {
    const { input, onChange } = renderSmart({ limit: 250 });
    expect(input).toHaveValue(250);
    fireEvent.change(input, { target: { value: "" } });
    fireEvent.blur(input);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange.mock.calls[0]?.[0].rules.limit).toBeUndefined();
  });

  it.each(["0", "-5", "2.5", "1e2"])("keeps the limit when %s is entered", (value) => {
    const { input, onChange } = renderSmart({ limit: 250 });
    fireEvent.change(input, { target: { value } });
    fireEvent.blur(input);
    expect(onChange).not.toHaveBeenCalled();
    expect(input).toHaveValue(250);
  });

  it("names each direction for the sort it orders", () => {
    renderSmart();
    expect(screen.getByRole("combobox", { name: "Direction" })).toHaveTextContent("Newest first");
    fireEvent.pointerDown(screen.getByRole("combobox", { name: "Direction" }), {
      button: 0,
      ctrlKey: false,
      pointerType: "mouse",
    });
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Newest first",
      "Oldest first",
    ]);
  });

  it("offers personal sorts only where they apply", () => {
    renderSmart({ allowPersonalized: true });
    fireEvent.pointerDown(screen.getByRole("combobox", { name: "Sort by" }), {
      button: 0,
      ctrlKey: false,
      pointerType: "mouse",
    });
    expect(screen.getByRole("option", { name: "Date Viewed" })).toBeInTheDocument();
  });

  it("changing the sort leaves sort_config alone when nothing is stored", () => {
    const { onChange } = renderSmart({ sortConfig: { mode: "manual_pins" } });
    choose(screen.getByRole("combobox", { name: "Sort by" }), "Title");
    expect(onChange).toHaveBeenCalledWith({
      rules: expect.objectContaining({ sort: { field: "title", order: "asc" } }),
      sortConfig: undefined,
    });
  });

  it("names a stored default sort and clears only its field and order", () => {
    const { onChange } = renderSmart({
      sortConfig: { field: "release_date", order: "asc", mode: "manual_pins" },
    });
    expect(
      screen.getByText("A saved default sort, Release Date, oldest first, wins over this Order."),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Clear the saved default sort/ }));
    expect(onChange).toHaveBeenCalledWith({ rules, sortConfig: { mode: "manual_pins" } });
  });
});

describe("OrderBlock, Synced list", () => {
  function renderSynced({
    limit,
    sortConfig = {},
  }: { limit?: number; sortConfig?: Record<string, unknown> } = {}) {
    const onSortChange = vi.fn<(sortConfig: Record<string, unknown>) => void>();
    const onLimitChange = vi.fn<(limit: number | undefined) => void>();
    render(
      <OrderBlock
        mode="synced"
        sortConfig={sortConfig}
        limit={limit}
        allowPersonalized={false}
        onSortChange={onSortChange}
        onLimitChange={onLimitChange}
      />,
    );
    return {
      input: screen.getByRole("spinbutton", { name: "Max titles" }),
      onSortChange,
      onLimitChange,
    };
  }

  it("keeps the list's own order by default and says blank takes the whole list", () => {
    const { input } = renderSynced();
    expect(screen.getByRole("combobox", { name: "Default sort" })).toHaveTextContent("List order");
    expect(input).toHaveAttribute("placeholder", "Whole list");
    expect(screen.getByText(/Blank takes the whole list, up to 500\./)).toBeInTheDocument();
  });

  it("sends the chosen default sort", () => {
    const { onSortChange } = renderSynced();
    choose(screen.getByRole("combobox", { name: "Default sort" }), "Title");
    expect(onSortChange).toHaveBeenCalledWith({ field: "title", order: "asc" });
  });

  it("caps max titles at 500 and clears it when blank", () => {
    const { input, onLimitChange } = renderSynced({ limit: 50 });
    fireEvent.change(input, { target: { value: "900" } });
    fireEvent.blur(input);
    expect(onLimitChange).toHaveBeenLastCalledWith(500);
    expect(input).toHaveValue(500);
    fireEvent.change(input, { target: { value: "" } });
    fireEvent.blur(input);
    expect(onLimitChange).toHaveBeenLastCalledWith(undefined);
  });
});
