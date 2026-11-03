import { useState } from "react";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { canSaveDraft } from "@/lib/homeRows/rowDraft";
import { HolidayFields } from "./SeasonalFields";
import seasonalThemes from "../../../../../contracts/api/v2/fixtures/list_seasonal_themes_ok.json";

// The real catalog body the server's contract fixture pins.
vi.mock("@/hooks/queries/seasonalThemes", () => ({
  useSeasonalThemes: () => ({ data: seasonalThemes, isError: false }),
}));

// Radix Select drives pointer capture and scrolling that jsdom lacks.
beforeAll(() => {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});

afterEach(cleanup);

type Config = Record<string, unknown>;

/** Renders the Holidays fields over a config and exposes what they last saved. */
function renderHolidays(initial: Config = { enabled_themes: ["halloween", "saturday_morning"] }) {
  const latest = { config: initial };
  function Harness() {
    const [config, setConfig] = useState(initial);
    return (
      <HolidayFields
        config={config}
        onChange={(next) => {
          latest.config = next;
          setConfig(next);
        }}
      />
    );
  }
  render(<Harness />);
  const canSave = () =>
    canSaveDraft({
      sectionType: "seasonal_themed",
      title: "Seasonal Picks",
      titleFollowsVariant: false,
      config: latest.config,
      itemLimit: 20,
      hero: false,
    });
  return { latest, canSave };
}

describe("Holiday fields", () => {
  it("picks a vibe per holiday, names the row after it, and can skip recommendations", async () => {
    const { latest } = renderHolidays();
    const name = screen.getByRole("textbox", { name: "Row name during Halloween" });
    expect(name).toHaveAttribute(
      "placeholder",
      'Name in season (defaults to "Halloween Favorites")',
    );
    await userEvent.click(screen.getByRole("combobox", { name: "Vibe for Halloween" }));
    await userEvent.click(await screen.findByRole("option", { name: "Family Halloween" }));
    expect(name).toHaveAttribute("placeholder", 'Name in season (defaults to "Family Halloween")');
    await userEvent.click(screen.getByRole("combobox", { name: "Picks from" }));
    await userEvent.click(await screen.findByRole("option", { name: "The lists only" }));
    expect(latest.config).toMatchObject({ vibes: { halloween: "family" }, source: "lists" });
  });

  it("moves a holiday to dates of the user's own, except weekly ones", async () => {
    const { latest, canSave } = renderHolidays();
    expect(screen.getByText("Leave empty for the usual dates (October).")).toBeInTheDocument();
    expect(
      screen.queryByRole("textbox", { name: "Saturday Morning starts (MM-DD)" }),
    ).not.toBeInTheDocument();
    await userEvent.type(
      screen.getByRole("textbox", { name: "Halloween starts (MM-DD)" }),
      "10-20",
    );
    expect(screen.getByText(/Enter both dates as MM-DD/)).toBeInTheDocument();
    expect(canSave()).toBe(false);
    await userEvent.type(screen.getByRole("textbox", { name: "Halloween ends (MM-DD)" }), "11-02");
    expect(screen.getByText("Oct 20–Nov 2")).toBeInTheDocument();
    expect(canSave()).toBe(true);
    expect(latest.config.theme_dates).toEqual({ halloween: { start: "10-20", end: "11-02" } });
  });

  it("adds a holiday of its own and allows saving only once it is complete", async () => {
    const { latest, canSave } = renderHolidays();
    await userEvent.click(screen.getByRole("button", { name: "Add a holiday" }));
    expect(screen.getByText("Give the holiday a name.")).toBeInTheDocument();
    expect(canSave()).toBe(false);
    await userEvent.type(
      screen.getByRole("textbox", { name: "Name of holiday 1" }),
      "Fourth of July",
    );
    const holiday = screen.getByRole("group", { name: "Your holidays" });
    await userEvent.type(
      within(holiday).getByRole("textbox", { name: "Fourth of July starts (MM-DD)" }),
      "07-01",
    );
    await userEvent.type(
      within(holiday).getByRole("textbox", { name: "Fourth of July ends (MM-DD)" }),
      "07-04",
    );
    // Paste the links: typing them key by key is slow under a full parallel run.
    const list = within(holiday).getByRole("textbox", { name: "Fourth of July MDBList list" });
    await userEvent.click(list);
    await userEvent.paste("https://example.com/lists/a/b");
    expect(screen.getByText(/Use a public MDBList list link/)).toBeInTheDocument();
    expect(canSave()).toBe(false);
    await userEvent.clear(list);
    await userEvent.paste("https://mdblist.com/lists/a/fourth");
    expect(canSave()).toBe(true);
    expect(latest.config.custom_holidays).toEqual([
      {
        name: "Fourth of July",
        start: "07-01",
        end: "07-04",
        list_url: "https://mdblist.com/lists/a/fourth",
      },
    ]);
  });
});
