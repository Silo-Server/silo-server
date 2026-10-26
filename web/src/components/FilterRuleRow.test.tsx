import { useState } from "react";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, it, vi } from "vitest";

import type { FilterRule } from "@/api/types";

import { FilterRuleRow, getFilterRuleFieldOptions } from "./FilterRuleEditor";

beforeAll(() => {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});

let latest: FilterRule | undefined;

function Row({
  initial,
  personalized = true,
  extendedRules = true,
}: {
  initial: FilterRule;
  personalized?: boolean;
  extendedRules?: boolean;
}) {
  const [rule, setRule] = useState(initial);
  return (
    <FilterRuleRow
      rule={rule}
      fieldOptions={getFilterRuleFieldOptions(personalized, "movie", undefined, { extendedRules })}
      allowPersonalizedFilters={personalized}
      onChange={(updates) => {
        const next = { ...rule, ...updates };
        latest = next;
        setRule(next);
      }}
      onRemove={() => {}}
      roomy
    />
  );
}

function renderRow(initial: FilterRule, personalized = true, extendedRules = true) {
  latest = undefined;
  return render(
    <Row initial={initial} personalized={personalized} extendedRules={extendedRules} />,
  );
}

function open(combobox: HTMLElement) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  return screen.getByRole("listbox");
}

function choose(combobox: HTMLElement, option: string) {
  open(combobox);
  fireEvent.click(screen.getByRole("option", { name: option }));
}

function optionNames(combobox: HTMLElement): string[] {
  const names = within(open(combobox))
    .getAllByRole("option")
    .map((option) => option.textContent ?? "");
  fireEvent.keyDown(screen.getByRole("listbox"), { key: "Escape" });
  return names;
}

const field = () => screen.getByRole("combobox", { name: "Field" });
const condition = () => screen.getByRole("combobox", { name: "Condition" });

describe("FilterRuleRow", () => {
  it("leaves out the You fields where personalized rules aren't allowed", () => {
    renderRow({ field: "genre", op: "is", value: "" }, false);
    expect(within(open(field())).queryByRole("group", { name: "You" })).toBeNull();
    expect(screen.queryByRole("option", { name: "Watched" })).toBeNull();
  });

  it("offers Status and genre “contains” only to a rule that already uses them", () => {
    const { unmount } = renderRow({ field: "genre", op: "is", value: "Drama" });
    expect(optionNames(field())).not.toContain("Status");
    expect(optionNames(condition())).toEqual(["is", "is not"]);
    unmount();

    renderRow({ field: "genre", op: "contains", value: "Drama" });
    expect(condition()).toHaveTextContent("contains");
    expect(optionNames(condition())).toEqual(["is", "is not", "contains"]);
    expect(screen.getByRole("combobox", { name: "Value" })).toHaveTextContent("Drama");
  });

  it("offers the newer fields and “is not in the last” only to a server that takes them", () => {
    renderRow({ field: "release_date", op: "in_last", value: "30d" }, true, false);
    const fields = optionNames(field());
    for (const name of ["Title", "Decade", "Duration", "TMDB rating", "RT critic score"]) {
      expect(fields).not.toContain(name);
    }
    expect(fields).toContain("IMDb rating");
    expect(optionNames(condition())).toEqual([
      "is in the last",
      "is before",
      "is after",
      "is between",
    ]);
  });

  it("keeps a saved rule an older server wouldn't offer editable", () => {
    renderRow({ field: "release_date", op: "not_in_last", value: "1y" }, true, false);
    expect(condition()).toHaveTextContent("is not in the last");
    cleanup();
    renderRow({ field: "title", op: "begins_with", value: "The " }, true, false);
    expect(field()).toHaveTextContent("Title");
    expect(optionNames(condition())).toContain("ends with");
  });

  it("keeps a saved Status rule editable", () => {
    renderRow({ field: "status", op: "is", value: "unmatched" });
    expect(field()).toHaveTextContent("Status");
    expect(screen.getByRole("combobox", { name: "Value" })).toHaveTextContent("unmatched");
    expect(optionNames(field())).toContain("Status");
    expect(latest).toBeUndefined();
  });

  it("answers yes or no for a switch, and stores a boolean", () => {
    renderRow({ field: "hdr", op: "is", value: false });
    const value = screen.getByRole("combobox", { name: "Value" });
    expect(value).toHaveTextContent("No");
    choose(value, "Yes");
    expect(latest).toEqual({ field: "hdr", op: "is", value: true });
  });

  describe("dates", () => {
    it("starts a new date rule at “is in the last”", () => {
      renderRow({ field: "genre", op: "is", value: "" });
      choose(field(), "Added");
      expect(latest).toEqual({ field: "added_at", op: "in_last", value: "" });
      expect(screen.getByRole("spinbutton", { name: "Amount" })).toBeInTheDocument();
    });

    it("takes a calendar date after or before", () => {
      renderRow({ field: "added_at", op: "gt", value: "2024-01-31" });
      expect(condition()).toHaveTextContent("is after");
      const input = screen.getByLabelText("Value");
      expect(input).toHaveAttribute("type", "date");
      expect(input).toHaveValue("2024-01-31");
      fireEvent.change(input, { target: { value: "2025-06-01" } });
      expect(latest?.value).toBe("2025-06-01");
    });

    it("takes two dates between", () => {
      renderRow({ field: "release_date", op: "between", value: ["1990-01-01", ""] });
      const to = screen.getByLabelText("To");
      expect(screen.getByLabelText("From")).toHaveValue("1990-01-01");
      expect(to).toHaveAttribute("type", "date");
      fireEvent.change(to, { target: { value: "1999-12-31" } });
      expect(latest?.value).toEqual(["1990-01-01", "1999-12-31"]);
    });

    it("keeps a saved date that isn't YYYY-MM-DD as text", () => {
      renderRow({ field: "added_at", op: "lt", value: "2024-01-31T00:00:00Z" });
      const input = screen.getByLabelText("Value");
      expect(input).toHaveAttribute("type", "text");
      expect(input).toHaveValue("2024-01-31T00:00:00Z");
    });

    it("writes “in the last” as a number and a unit", () => {
      renderRow({ field: "added_at", op: "in_last", value: "30d" });
      const amount = screen.getByRole("spinbutton", { name: "Amount" });
      const unit = screen.getByRole("combobox", { name: "Unit" });
      expect(amount).toHaveValue(30);
      expect(unit).toHaveTextContent("days");
      expect(optionNames(unit)).toEqual(["days", "weeks", "months", "years"]);

      fireEvent.change(amount, { target: { value: "6" } });
      expect(latest?.value).toBe("6d");
      choose(unit, "months");
      expect(latest?.value).toBe("6m");
    });

    it("remembers the unit while the number is blank", () => {
      renderRow({ field: "release_date", op: "in_last", value: "" });
      const amount = screen.getByRole("spinbutton", { name: "Amount" });
      expect(amount).toHaveValue(null);
      choose(screen.getByRole("combobox", { name: "Unit" }), "years");
      expect(latest?.value).toBe("");
      fireEvent.change(amount, { target: { value: "2" } });
      expect(latest?.value).toBe("2y");
    });

    it("writes nothing for an amount below 1", () => {
      renderRow({ field: "added_at", op: "in_last", value: "30d" });
      fireEvent.change(screen.getByRole("spinbutton", { name: "Amount" }), {
        target: { value: "0" },
      });
      expect(latest?.value).toBe("");
    });

    it("keeps hours for a saved value in hours", () => {
      renderRow({ field: "added_at", op: "in_last", value: "36h" });
      expect(screen.getByRole("spinbutton", { name: "Amount" })).toHaveValue(36);
      const unit = screen.getByRole("combobox", { name: "Unit" });
      expect(unit).toHaveTextContent("hours");
      expect(optionNames(unit)).toEqual(["hours", "days", "weeks", "months", "years"]);
      expect(latest).toBeUndefined();
    });

    it("keeps a saved value it can't read as text, so nothing is lost", () => {
      renderRow({ field: "added_at", op: "in_last", value: "last month" });
      const input = screen.getByRole("textbox", { name: "Value" });
      expect(input).toHaveValue("last month");
      expect(screen.queryByRole("spinbutton", { name: "Amount" })).toBeNull();
      expect(latest).toBeUndefined();
    });

    it("starts over when the condition changes between a date and a span", () => {
      renderRow({ field: "added_at", op: "gt", value: "2024-01-31" });
      choose(condition(), "is in the last");
      expect(latest).toEqual({ field: "added_at", op: "in_last", value: "" });
      expect(screen.getByRole("spinbutton", { name: "Amount" })).toHaveValue(null);
    });

    it("keeps the span when the condition flips to “is not in the last”", () => {
      renderRow({ field: "latest_episode_added", op: "in_last", value: "2w" });
      choose(condition(), "is not in the last");
      expect(latest).toEqual({ field: "latest_episode_added", op: "not_in_last", value: "2w" });
      expect(screen.getByRole("spinbutton", { name: "Amount" })).toHaveValue(2);
      expect(screen.getByRole("combobox", { name: "Unit" })).toHaveTextContent("weeks");
    });

    it("keeps a date range's start when the condition changes to after", () => {
      renderRow({ field: "release_date", op: "between", value: ["1990-01-01", "1999-12-31"] });
      choose(condition(), "is after");
      expect(latest).toEqual({ field: "release_date", op: "gt", value: "1990-01-01" });
    });
  });

  it("matches a title by part of it, starting with “contains”", () => {
    renderRow({ field: "genre", op: "is", value: "" });
    choose(field(), "Title");
    expect(latest).toEqual({ field: "title", op: "contains", value: "" });
    expect(optionNames(condition())).toEqual([
      "contains",
      "does not contain",
      "is",
      "is not",
      "begins with",
      "ends with",
    ]);
    fireEvent.change(screen.getByRole("textbox", { name: "Value" }), {
      target: { value: "Star" },
    });
    expect(latest?.value).toBe("Star");
    choose(condition(), "begins with");
    expect(latest).toEqual({ field: "title", op: "begins_with", value: "Star" });
  });

  it("picks a decade and stores its first year as a number", () => {
    renderRow({ field: "decade", op: "is", value: "" });
    const value = screen.getByRole("combobox", { name: "Value" });
    const decades = optionNames(value);
    expect(decades[decades.length - 1]).toBe("1900s");
    expect(decades).toContain("1980s");
    choose(value, "1980s");
    expect(latest).toEqual({ field: "decade", op: "is", value: 1980 });
  });

  it("moves focus to the value control when asked", async () => {
    render(
      <FilterRuleRow
        rule={{ field: "genre", op: "is", value: "" }}
        fieldOptions={getFilterRuleFieldOptions(false)}
        allowPersonalizedFilters={false}
        onChange={vi.fn()}
        onRemove={vi.fn()}
        focusValue
      />,
    );
    expect(screen.getByRole("combobox", { name: "Value" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Remove rule" })).toHaveFocus();
  });
});
