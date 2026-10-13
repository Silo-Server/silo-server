import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import type { FacetValueScope } from "@/hooks/queries/facetValues";

import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./dialog";
import { FacetValuePicker } from "./facet-value-picker";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
installV2Recorder();

const CAPABILITIES = "GET /api/v2/catalog/search/capabilities";
const SEARCH = "GET /api/v2/catalog/filters/search";

const STUDIOS = [
  { value: "Warner Bros. Pictures", count: 1203 },
  { value: "A24", count: 41 },
  { value: "Aardman", count: 1 },
];

/**
 * Answers like the server: values by case-insensitive word starts, most
 * titles first; matches only whole-value prefixes, A to Z.
 */
function answerStudios(call: RecordedCall) {
  const q = String(call.query?.q ?? "").toLowerCase();
  const values = STUDIOS.filter(({ value }) => {
    const lowered = value.toLowerCase();
    return lowered.startsWith(q) || lowered.split(/[^a-z0-9]+/).some((w) => w.startsWith(q));
  });
  const matches =
    q === ""
      ? []
      : STUDIOS.map((entry) => entry.value)
          .filter((value) => value.toLowerCase().startsWith(q))
          .sort();
  return { matches, has_more: false, values, values_has_more: false };
}

function serve({ ranked = true } = {}) {
  v2Recorder.answer(CAPABILITIES, {
    revision: "r1",
    state: "available",
    allowed: true,
    facet_value_search: ranked,
  });
  v2Recorder.answer(SEARCH, answerStudios);
}

let picked: string | undefined;

function Harness({ initial = "", scope }: { initial?: string; scope?: FacetValueScope }) {
  const [value, setValue] = useState(initial);
  return (
    <FacetValuePicker
      facet="studio"
      value={value}
      onChange={(next) => {
        picked = next;
        setValue(next);
      }}
      scope={scope}
      placeholder="Pick a studio"
      searchLabel="Search studios"
    />
  );
}

function renderPicker(props: { initial?: string; scope?: FacetValueScope } = {}) {
  picked = undefined;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <Harness {...props} />
    </QueryClientProvider>,
  );
  // The open popover is modal, which hides the trigger from role queries.
  return () => screen.getByRole("combobox", { name: "Value", hidden: true });
}

function searches() {
  return v2Recorder.callsOf(SEARCH).map((call) => call.query);
}

describe("FacetValuePicker", () => {
  it("opens on the most common values in the rule's scope, with title counts", async () => {
    serve();
    const trigger = renderPicker({ scope: { libraryIds: [3, 1], mediaScope: "movie" } });
    expect(trigger()).toHaveTextContent("Pick a studio");
    expect(v2Recorder.calls).toEqual([]);

    await userEvent.click(trigger());
    const listbox = await screen.findByRole("listbox", { name: "Search studios" });
    expect(
      await within(listbox).findByRole("option", { name: "Warner Bros. Pictures, 1,203 titles" }),
    ).toBeInTheDocument();
    expect(within(listbox).getByRole("option", { name: "Aardman, 1 title" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Search studios" })).toHaveFocus();
    expect(searches()).toEqual([
      {
        source: "query",
        facet: "studio",
        q: "",
        limit: 50,
        type: "movie",
        library_ids: ["1", "3"],
      },
    ]);
  });

  it("searches what is typed and sets the picked value", async () => {
    serve();
    const trigger = renderPicker({ scope: { libraryIds: [], mediaScope: "all" } });
    await userEvent.click(trigger());
    await userEvent.type(screen.getByRole("combobox", { name: "Search studios" }), "bro");
    await waitFor(() => expect(searches().at(-1)).toMatchObject({ q: "bro" }));
    // "all" and no libraries search every title the viewer can see.
    expect(searches().at(-1)).not.toHaveProperty("type");
    expect(searches().at(-1)).not.toHaveProperty("library_ids");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));

    await userEvent.click(screen.getByRole("option", { name: /^Warner Bros\. Pictures/ }));
    expect(picked).toBe("Warner Bros. Pictures");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(trigger()).toHaveTextContent("Warner Bros. Pictures");
    expect(trigger()).toHaveFocus();
  });

  it("offers typed text no title has yet as a value of its own", async () => {
    serve();
    const trigger = renderPicker();
    await userEvent.click(trigger());
    await userEvent.type(screen.getByRole("combobox", { name: "Search studios" }), "Warner Bros");
    const typed = await screen.findByRole("option", { name: /^Use “Warner Bros”/ });
    await waitFor(() => expect(typed).toHaveTextContent("No titles have this yet"));
    // Rules match exactly, so a listed value is never offered again as typed text.
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Warner Bros. Pictures1,203",
      "Use “Warner Bros”No titles have this yet",
    ]);
    await userEvent.click(typed);
    expect(picked).toBe("Warner Bros");
  });

  it("keeps a saved value the list doesn't have, pinned first and selected", async () => {
    serve();
    const trigger = renderPicker({ initial: "Old Studio" });
    expect(trigger()).toHaveTextContent("Old Studio");
    await userEvent.click(trigger());
    await screen.findByRole("option", { name: /^A24/ });
    const options = screen.getAllByRole("option");
    expect(options[0]).toHaveTextContent("Old Studio");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(options).toHaveLength(4);
  });

  it("works from the keyboard", async () => {
    serve();
    const trigger = renderPicker();
    trigger().focus();
    await userEvent.keyboard("{ArrowDown}");
    const search = screen.getByRole("combobox", { name: "Search studios" });
    await screen.findByRole("option", { name: /^A24/ });
    const active = () =>
      document.getElementById(search.getAttribute("aria-activedescendant") ?? "")?.textContent;

    expect(active()).toMatch(/^Warner Bros\. Pictures/);
    await userEvent.keyboard("{ArrowDown}");
    expect(active()).toMatch(/^A24/);
    await userEvent.keyboard("{End}");
    expect(active()).toMatch(/^Aardman/);
    await userEvent.keyboard("{Home}");
    expect(active()).toMatch(/^Warner/);
    await userEvent.keyboard("{ArrowDown}{Enter}");
    expect(picked).toBe("A24");
    expect(trigger()).toHaveFocus();

    await userEvent.keyboard("{ArrowDown}");
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(trigger()).toHaveFocus();
    expect(picked).toBe("A24");
  });

  it("uses typed text on Enter before the list has answered it", async () => {
    serve();
    const trigger = renderPicker();
    await userEvent.click(trigger());
    await screen.findByRole("option", { name: /^Warner Bros\. Pictures/ });

    // Enter lands before the search for "Aard" is sent, while the list on
    // screen still answers the empty search.
    await userEvent.type(screen.getByRole("combobox", { name: "Search studios" }), "Aard{Enter}");
    expect(picked).toBe("Aard");
    expect(trigger()).toHaveTextContent("Aard");
  });

  it("starts a search with a letter typed on the closed picker", async () => {
    serve();
    const trigger = renderPicker();
    trigger().focus();
    await userEvent.keyboard("a");
    expect(screen.getByRole("combobox", { name: "Search studios" })).toHaveValue("a");
    await waitFor(() => expect(searches().at(-1)).toMatchObject({ q: "a" }));
  });

  it("works inside a dialog, and Escape closes only the picker", async () => {
    serve();
    picked = undefined;
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <Dialog open>
          <DialogContent>
            <DialogTitle>Add row</DialogTitle>
            <DialogDescription>Rules</DialogDescription>
            <Harness />
          </DialogContent>
        </Dialog>
      </QueryClientProvider>,
    );
    await userEvent.click(screen.getByRole("combobox", { name: "Value" }));
    expect(screen.getByRole("combobox", { name: "Search studios" })).toHaveFocus();
    await userEvent.click(await screen.findByRole("option", { name: /^A24/ }));
    expect(picked).toBe("A24");

    await userEvent.click(screen.getByRole("combobox", { name: "Value" }));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.getByRole("dialog", { name: "Add row" })).toBeInTheDocument();
  });

  it("asks to narrow the search when the ranked values ran over the limit", async () => {
    serve();
    // has_more speaks for matches, which a ranked server's picker doesn't read.
    v2Recorder.answer(SEARCH, (call: RecordedCall) => ({
      ...answerStudios(call),
      has_more: false,
      values_has_more: true,
    }));
    const trigger = renderPicker();
    await userEvent.click(trigger());
    expect(await screen.findByText("Keep typing to narrow")).toBeInTheDocument();
  });

  it("only searches typed text on a server without ranked value search", async () => {
    serve({ ranked: false });
    v2Recorder.answer(SEARCH, (call: RecordedCall) => ({
      matches: ["Drama", "Dramedy"],
      values: [],
      has_more: false,
      q: call.query?.q,
    }));
    const trigger = renderPicker({ scope: { libraryIds: [4], mediaScope: "series" } });
    await userEvent.click(trigger());
    expect(await screen.findByText("Type to search")).toBeInTheDocument();
    expect(searches()).toEqual([]);

    await userEvent.type(screen.getByRole("combobox", { name: "Search studios" }), "dr");
    expect(await screen.findByRole("option", { name: "Drama" })).toBeInTheDocument();
    // Names only, without counts, and no library_ids an older server may refuse.
    expect(searches()).toEqual([
      { source: "query", facet: "studio", q: "dr", limit: 50, type: "series" },
    ]);
  });
});
