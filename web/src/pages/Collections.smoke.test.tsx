/**
 * Smoke tests for the Collections page: the Your collections grid moves
 * with the keyboard and announces the move, Shared with me cards carry no
 * buttons (#195 W7), and a single-profile account still sees Server
 * collections (#195 W1).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import getLibraryCollectionsOk from "../../../contracts/api/v2/fixtures/get_library_collections_ok.json";
import listCollectionsOk from "../../../contracts/api/v2/fixtures/list_collections_ok.json";
import { personalCapabilities } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import Collections from "./Collections";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
const account = vi.hoisted(() => ({
  profiles: [] as Array<{ id: string; name: string }>,
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: account.profiles }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" } }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium" } }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

installV2Recorder();

const roadTrips = { ...getCollectionOk, id: "c3", name: "Road trips" };

const rects = new Map<Element, DOMRect>();

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
      takeRecords() {
        return [];
      }
    },
  );
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }));
  HTMLElement.prototype.scrollIntoView = () => {};
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    return rects.get(this) ?? new DOMRect(0, 0, 0, 0);
  });
  account.profiles = [
    { id: "p-owner", name: "Owner" },
    { id: "p-primary", name: "Parent" },
  ];
  v2Recorder.answer("GET /api/v2/collections/capabilities", {
    ...personalCapabilities,
    imports: false,
  });
  v2Recorder.answer("GET /api/v2/collections", {
    items: [...listCollectionsOk.items, roadTrips],
  });
  v2Recorder.answer("GET /api/v2/collections/server", { libraries: [] });
  v2Recorder.answer("GET /api/v2/collections/order", {
    group_id: null,
    ordered_ids: ["c1", "c3"],
  });
  v2Recorder.answer(
    "PUT /api/v2/collections/order",
    ({ body }: { body: { ordered_ids: string[] } }) => ({
      group_id: null,
      ordered_ids: body.ordered_ids,
    }),
  );
});

afterEach(() => {
  rects.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
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
      <MemoryRouter>
        <Collections />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** Everything the drag-and-drop live region says, in order, from now on. */
function recordAnnouncements() {
  const said: string[] = [];
  const observer = new MutationObserver(() => {
    const text = document.querySelector("[aria-live]")?.textContent ?? "";
    if (text && said.at(-1) !== text) said.push(text);
  });
  observer.observe(document.body, { subtree: true, childList: true, characterData: true });
  return said;
}

describe("Your collections", () => {
  it("moves a collection with the keyboard, announces it, and saves the order", async () => {
    show();
    const handle = await screen.findByRole("button", { name: "Drag Rainy days" });
    const other = screen.getByRole("button", { name: "Drag Road trips" });
    // jsdom lays nothing out: put the two cards side by side.
    rects.set(handle.closest("[data-collection-id]")!, new DOMRect(0, 0, 300, 100));
    rects.set(other.closest("[data-collection-id]")!, new DOMRect(320, 0, 300, 100));
    expect(document.getElementById(handle.getAttribute("aria-describedby")!)).toHaveTextContent(
      "To pick up a draggable item, press the space bar.",
    );

    const announcements = recordAnnouncements();
    handle.focus();
    await act(async () => {
      fireEvent.keyDown(handle, { code: "Space" });
      // The sensor starts listening for arrows on the next task.
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await act(async () => {
      fireEvent.keyDown(handle, { code: "ArrowRight" });
    });
    await act(async () => {
      fireEvent.keyDown(handle, { code: "Space" });
    });
    // The pick-up announcement is replaced in the same render by the first
    // "is over", so assistive technology hears these three, by name and
    // position.
    expect(announcements).toEqual([
      "Rainy days is over position 1 of 2.",
      "Rainy days is over position 2 of 2.",
      "Moved Rainy days to position 2 of 2.",
    ]);

    await vi.waitFor(() => expect(v2Recorder.writes()).toHaveLength(1));
    expect(v2Recorder.writes()).toEqual([
      {
        operation: "PUT /api/v2/collections/order",
        path: "/api/v2/collections/order",
        headers: { "If-Match": '"/api/v2/collections/order#1"' },
        body: { ordered_ids: ["c3", "c1"] },
      },
    ]);
  });
});

describe("New collection on a narrow screen", () => {
  // jsdom lays nothing out: the dock's class is what keeps it above the
  // audiobook or watch bar that plays in the background, as SaveBar's does.
  it("docks above the background playback bar", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(max-width: 1023px)",
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    show();
    const dock = await screen.findByRole("region", { name: "Page actions" });
    expect(within(dock).getByRole("button", { name: "New collection" })).toBeInTheDocument();
    expect(dock).toHaveClass("fixed", "bottom-(--playback-bar-clearance,0px)");
    expect(dock).not.toHaveClass("bottom-0");
  });
});

describe("Shared with me", () => {
  it("shows another profile's shared collection as a card with no buttons", async () => {
    show();
    const shared = await screen.findByRole("region", { name: "Shared with me" });
    expect(within(shared).getByRole("link", { name: "Family night" })).toBeInTheDocument();
    expect(within(shared).queryAllByRole("button")).toEqual([]);
  });
});

describe("Server collections", () => {
  it("shows on a single-profile account", async () => {
    account.profiles = [{ id: "p-owner", name: "Owner" }];
    v2Recorder.answer("GET /api/v2/collections", { items: [getCollectionOk] });
    v2Recorder.answer("GET /api/v2/collections/server", {
      libraries: [
        {
          library_id: "1",
          library_name: "Movies",
          total_count: 1,
          collections: getLibraryCollectionsOk.ungrouped.collections,
        },
      ],
    });
    show();
    // The library's cards under the Server collections heading, once loaded,
    // with See all leading to its Collections tab.
    const server = await screen.findByRole("region", { name: "Server collections" });
    expect(
      await within(server).findByRole("link", { name: "See all 1 Movies collections" }),
    ).toHaveAttribute("href", "/library/1?tab=collections");
    expect(within(server).getByRole("link", { name: /Oscar Winners/ })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Shared with me" })).toBeNull();
  });
});
