/**
 * Your collections cards drag from anywhere on the card, so the
 * card must not open after a drag, a press inside its ⋯ menu must not drag it,
 * and its confirm dialogs hand focus back to the ⋯ that opened them.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import listCollectionsOk from "../../../contracts/api/v2/fixtures/list_collections_ok.json";
import { personalCapabilities } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import Collections from "./Collections";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({
    data: [
      { id: "p-owner", name: "Owner" },
      { id: "p-kid", name: "Leo" },
    ],
  }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" } }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium", caption: "title" } }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

installV2Recorder();

const [rainyDays, familyNight] = listCollectionsOk.items;

beforeEach(() => {
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
  v2Recorder.answer("GET /api/v2/collections", {
    items: [{ ...rainyDays, is_shared: true }, familyNight],
  });
  v2Recorder.answer("GET /api/v2/collections/server", { libraries: [] });
  v2Recorder.answer("GET /api/v2/collections/order", { ordered_ids: ["c1", "c2"] });
});

function show() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
        })
      }
    >
      <MemoryRouter initialEntries={["/collections"]}>
        <Routes>
          <Route path="/collections" element={<Collections />} />
          <Route path="*" element={<p>Somewhere else</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const pointer = { isPrimary: true, button: 0, pointerId: 1, pointerType: "mouse" };

/**
 * Presses on target, moves well past the drag threshold, and releases. Each
 * step is its own act(), so React renders between them as a browser would.
 */
function pointerDrag(target: Element) {
  act(() => void fireEvent.pointerDown(target, { ...pointer, clientX: 10, clientY: 10 }));
  act(() => void fireEvent.pointerMove(document, { ...pointer, clientX: 60, clientY: 10 }));
  act(() => void fireEvent.pointerMove(document, { ...pointer, clientX: 120, clientY: 10 }));
  act(() => void fireEvent.pointerUp(document, { ...pointer, clientX: 120, clientY: 10 }));
}

function cardOf(name: string) {
  return screen.getByRole("button", { name: `More for ${name}` }).closest("li")!;
}

describe("Dragging a Your collections card", () => {
  it("does not open the collection on the click that ends a pointer drag", async () => {
    show();
    await screen.findByRole("button", { name: "More for Rainy days" });
    const link = within(cardOf("Rainy days")).getAllByRole("link")[0]!;

    pointerDrag(link);
    expect(fireEvent.click(link)).toBe(false);
    expect(screen.queryByText("Somewhere else")).toBeNull();

    // Only that click: a moment later the card opens again.
    await vi.waitFor(() => {
      fireEvent.click(link);
      expect(screen.getByText("Somewhere else")).toBeInTheDocument();
    });
  });

  it("still opens the collection on a plain click", async () => {
    show();
    await screen.findByRole("button", { name: "More for Rainy days" });
    const link = within(cardOf("Rainy days")).getAllByRole("link")[0]!;

    fireEvent.click(link);
    expect(await screen.findByText("Somewhere else")).toBeInTheDocument();
  });

  it("does not drag the card when a press inside its ⋯ menu moves", async () => {
    show();
    await userEvent.click(await screen.findByRole("button", { name: "More for Rainy days" }));
    const remove = within(await screen.findByRole("menu")).getByRole("menuitem", {
      name: "Delete…",
    });

    pointerDrag(remove);
    expect(v2Recorder.operations()).not.toContain("GET /api/v2/collections/order");
    expect(cardOf("Rainy days")).toHaveStyle({ opacity: "1" });
  });

  it("does not drag the card when a press on its open ⋯ button moves", async () => {
    show();
    const trigger = await screen.findByRole("button", { name: "More for Rainy days" });
    await userEvent.click(trigger);
    await screen.findByRole("menu");

    // A press that closes the menu is not cancelled by the trigger, so only
    // the card can keep it from starting a drag.
    pointerDrag(trigger);
    expect(v2Recorder.operations()).not.toContain("GET /api/v2/collections/order");
    expect(cardOf("Rainy days")).toHaveStyle({ opacity: "1" });
  });

  it("does not drag the card when a touch starts on the card itself", async () => {
    show();
    await screen.findByRole("button", { name: "More for Rainy days" });
    const link = within(cardOf("Rainy days")).getAllByRole("link")[0]!;
    const touch = { ...pointer, pointerType: "touch" };

    // A finger that starts a scroll on the card moves a few pixels before the
    // browser takes over; that must not start a drag. Touch drags use the handle.
    act(() => void fireEvent.pointerDown(link, { ...touch, clientX: 10, clientY: 10 }));
    act(() => void fireEvent.pointerMove(document, { ...touch, clientX: 10, clientY: 60 }));
    expect(cardOf("Rainy days")).toHaveStyle({ opacity: "1" });
    act(() => void fireEvent.pointerCancel(document, { ...touch, clientX: 10, clientY: 60 }));
    expect(v2Recorder.operations()).not.toContain("GET /api/v2/collections/order");
  });

  it("still drags the card from its handle on touch", async () => {
    show();
    await screen.findByRole("button", { name: "More for Rainy days" });
    const touch = { ...pointer, pointerType: "touch" };

    act(() => {
      fireEvent.pointerDown(screen.getByRole("button", { name: "Drag Rainy days" }), {
        ...touch,
        clientX: 10,
        clientY: 10,
      });
    });
    act(() => void fireEvent.pointerMove(document, { ...touch, clientX: 60, clientY: 10 }));
    expect(cardOf("Rainy days")).toHaveStyle({ opacity: "0.4" });
    act(() => void fireEvent.pointerUp(document, { ...touch, clientX: 60, clientY: 10 }));
    await vi.waitFor(() => expect(fireEvent.click(document.body)).toBe(true));
  });

  it("still drags the card from its handle", async () => {
    show();
    await screen.findByRole("button", { name: "More for Rainy days" });

    act(() => {
      fireEvent.pointerDown(screen.getByRole("button", { name: "Drag Rainy days" }), {
        ...pointer,
        clientX: 10,
        clientY: 10,
      });
    });
    act(() => void fireEvent.pointerMove(document, { ...pointer, clientX: 60, clientY: 10 }));
    expect(cardOf("Rainy days")).toHaveStyle({ opacity: "0.4" });
    act(() => void fireEvent.pointerUp(document, { ...pointer, clientX: 60, clientY: 10 }));
    // Let the click guard that follows a pointer drag lift before the next test.
    await vi.waitFor(() => expect(fireEvent.click(document.body)).toBe(true));
  });
});

describe("Your collections confirm dialogs", () => {
  it("return focus to the card's ⋯ when Delete… is cancelled", async () => {
    show();
    const trigger = await screen.findByRole("button", { name: "More for Rainy days" });
    await userEvent.click(trigger);
    await userEvent.click(
      within(await screen.findByRole("menu")).getByRole("menuitem", { name: "Delete…" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    await vi.waitFor(() => expect(trigger).toHaveFocus());
  });

  it("keep the collection's name in their titles while they close", async () => {
    show();
    // Every text the dialogs ever rendered, including frames that a closing
    // dialog shows only until it unmounts.
    const titles: string[] = [];
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        if (record.type === "characterData") titles.push(record.target.nodeValue ?? "");
        for (const node of record.addedNodes) titles.push(node.textContent ?? "");
      }
    });
    observer.observe(document.body, { subtree: true, childList: true, characterData: true });

    for (const item of ["Delete…", "Show to other profiles"]) {
      await userEvent.click(await screen.findByRole("button", { name: "More for Rainy days" }));
      const menu = await screen.findByRole("menu");
      await userEvent.click(
        within(menu).getByRole(item === "Delete…" ? "menuitem" : "menuitemcheckbox", {
          name: item,
        }),
      );
      const dialog = await screen.findByRole("alertdialog");
      await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
      await vi.waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    }
    observer.disconnect();

    expect(titles.some((text) => text.includes('Delete "Rainy days"?'))).toBe(true);
    expect(titles.some((text) => text.includes("Stop sharing Rainy days?"))).toBe(true);
    expect(titles.filter((text) => text.includes("undefined"))).toEqual([]);
  });

  it("return focus to the card's ⋯ when stopping sharing is cancelled", async () => {
    show();
    const trigger = await screen.findByRole("button", { name: "More for Rainy days" });
    await userEvent.click(trigger);
    await userEvent.click(
      within(await screen.findByRole("menu")).getByRole("menuitemcheckbox", {
        name: "Show to other profiles",
      }),
    );
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.keyboard("{Escape}");
    expect(dialog).not.toBeInTheDocument();

    await vi.waitFor(() => expect(trigger).toHaveFocus());
  });
});
