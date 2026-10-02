// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import type { AdminUser } from "@/api/types";

import { ActivityTab } from "./ActivityTab";

const NOW = new Date("2026-09-30T12:00:00.000Z");
const DAY = 86_400_000;
const iso = (daysAgo: number) => new Date(NOW.getTime() - daysAgo * DAY).toISOString();

const mocks = vi.hoisted(() => ({
  capabilities: { account_devices: true, watch_summary: true } as Record<string, boolean>,
}));

vi.mock("@/hooks/queries/admin/users", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/admin/users")>()),
  useAdminUserCapabilities: () => ({ data: { available: true, ...mocks.capabilities } }),
}));

const user = { id: 7, username: "taylor" } as AdminUser;

function play(id: string, title: string, profileId = "p1") {
  return {
    session_id: id,
    user_id: "7",
    username: "taylor",
    profile_id: profileId,
    profile_name: profileId === "p1" ? "Main" : "Kids",
    media_item_id: `item-${id}`,
    media_file_id: "3",
    media_title: title,
    media_type: "movie",
    series_title: "",
    season_number: null,
    episode_number: null,
    play_method: "transcode",
    started_at: iso(2),
    ended_at: iso(2),
    watched_seconds: 600,
    duration_seconds: 1200,
    completed: false,
  };
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}

let requests: URL[] = [];

function route(url: URL): unknown {
  const done = { has_more: false };
  switch (url.pathname) {
    case "/api/v2/admin/users/7/profiles":
      return {
        items: [
          { id: "p1", name: "Main", last_seen_at: null },
          { id: "p2", name: "Kids", last_seen_at: null },
        ],
        page: done,
      };
    case "/api/v2/admin/playback-history":
      return url.searchParams.get("cursor") === "c2"
        ? { items: [play("h2", "Oppenheimer")], page: done }
        : { items: [play("h1", "Dune: Part Two")], page: { has_more: true, next_cursor: "c2" } };
    case "/api/v2/admin/users/7/watch-summary":
      return {
        days: Number(url.searchParams.get("days")),
        since: iso(30),
        plays: 41,
        completed_plays: 20,
        watched_seconds: 1000,
        last_played_at: null,
      };
    case "/api/v2/admin/sessions":
      return {
        items: [
          {
            session_id: "live-1",
            user_id: "7",
            media_file_id: "4",
            requested_media_file_id: "4",
            profile_id: "p1",
            profile_name: "Main",
            content_id: "severance",
            media_title: "Hello, Ms. Cobel",
            series_name: "Severance",
            season_number: 2,
            episode_number: 4,
            play_method: "direct",
            position_seconds: 300,
            file_duration: 3000,
          },
        ],
        page: done,
      };
    case "/api/v2/admin/users/7/devices":
      return {
        items: [
          {
            device_id: "dev-tv",
            device_name: "Apple TV 4K",
            device_platform: "tvOS",
            last_seen_at: iso(0.1),
            last_updated: iso(0.1),
            override_count: 2,
            profiles: [
              { profile_id: "p2", profile_name: "Kids", override_count: 0, last_seen_at: iso(1) },
              { profile_id: "p1", profile_name: "Main", override_count: 2, last_seen_at: iso(0.1) },
            ],
          },
          {
            device_id: "dev-old",
            device_name: "Old iPad",
            device_platform: "iPadOS",
            last_seen_at: iso(90),
            last_updated: iso(90),
            override_count: 0,
            profiles: [],
          },
        ],
        page: done,
      };
    case "/api/v2/admin/users/7/ips":
      return {
        items: [
          {
            client_ip: "192.168.1.40",
            first_seen: iso(29),
            last_seen: iso(0),
            request_count: 8214,
            location: "local",
          },
          {
            client_ip: "81.12.44.190",
            first_seen: "2026-09-25T10:00:00.000Z",
            last_seen: iso(5),
            request_count: 312,
            location: "remote",
          },
          {
            client_ip: "2a02:c7c:4d1::12",
            first_seen: iso(29.5),
            last_seen: iso(18),
            request_count: 96,
            location: "remote",
          },
        ],
        page: done,
      };
    default:
      throw new Error(`unexpected request ${url.pathname}`);
  }
}

class MockResizeObserver implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function renderTab() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/admin/users/7?tab=activity"]}>
        <ActivityTab user={user} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function historyRequests() {
  return requests.filter((url) => url.pathname === "/api/v2/admin/playback-history");
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  mocks.capabilities = { account_devices: true, watch_summary: true };
  requests = [];
  vi.stubGlobal("ResizeObserver", MockResizeObserver);
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input) => {
      const url = new URL(String(input), "http://localhost");
      requests.push(url);
      return json(route(url));
    }),
  );
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("ActivityTab watch history", () => {
  it("lists the live session first, then finalized plays, with the plays total", async () => {
    renderTab();
    const history = screen.getByRole("region", { name: "Watch history" });
    await within(history).findByText("Dune: Part Two");
    await within(history).findByText("Severance");

    const rows = within(history).getAllByRole("row").slice(1);
    expect(rows[0]).toHaveTextContent("Severance");
    expect(rows[0]).toHaveTextContent("S02E04");
    expect(rows[0]).toHaveTextContent("Now");
    expect(rows[1]).toHaveTextContent("Dune: Part Two");
    expect(within(history).getByText("41 plays in the last 30 days")).toBeInTheDocument();
    expect(within(history).getByText("Showing 1 of 41")).toBeInTheDocument();
    expect(within(history).queryByRole("combobox", { name: /device/i })).not.toBeInTheDocument();

    const first = historyRequests()[0]!;
    expect(first.searchParams.get("user_id")).toBe("7");
    expect(first.searchParams.get("ended_after")).toBe(iso(30));
  });

  it("loads the next page with the cursor and the same window", async () => {
    const u = userEvent.setup();
    renderTab();
    const history = screen.getByRole("region", { name: "Watch history" });
    await within(history).findByText("Dune: Part Two");

    await u.click(within(history).getByRole("button", { name: "Load more" }));
    await within(history).findByText("Oppenheimer");
    const [first, second] = historyRequests();
    expect(second!.searchParams.get("cursor")).toBe("c2");
    expect(second!.searchParams.get("ended_after")).toBe(first!.searchParams.get("ended_after"));
    expect(within(history).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("asks for the chosen period as ended_after", async () => {
    const u = userEvent.setup();
    renderTab();
    const history = screen.getByRole("region", { name: "Watch history" });
    await within(history).findByText("Dune: Part Two");

    await u.click(within(history).getByRole("combobox", { name: "Period" }));
    await u.click(await screen.findByRole("option", { name: "Last 7 days" }));

    await waitFor(() =>
      expect(historyRequests().at(-1)!.searchParams.get("ended_after")).toBe(iso(7)),
    );
    await within(history).findByText("41 plays in the last 7 days");
    const summary = requests.filter((url) => url.pathname.endsWith("/watch-summary")).at(-1)!;
    expect(summary.searchParams.get("days")).toBe("7");
  });

  it("passes the profile filter to history and the summary", async () => {
    const u = userEvent.setup();
    renderTab();
    const history = screen.getByRole("region", { name: "Watch history" });
    await within(history).findByText("Dune: Part Two");

    await u.click(within(history).getByRole("combobox", { name: "Profile" }));
    await u.click(await screen.findByRole("option", { name: "Kids" }));

    await waitFor(() =>
      expect(historyRequests().at(-1)!.searchParams.get("profile_id")).toBe("p2"),
    );
    const summary = requests.filter((url) => url.pathname.endsWith("/watch-summary")).at(-1)!;
    expect(summary.searchParams.get("profile_id")).toBe("p2");
    // The live session belongs to Main, so it drops out under the Kids filter.
    await waitFor(() => expect(within(history).queryByText("Severance")).not.toBeInTheDocument());
    expect(within(history).getByRole("link", { name: /Open in Playback History/ })).toHaveAttribute(
      "href",
      "/admin/history?user_id=7&profile_id=p2",
    );
  });

  it("omits the plays total without the watch summary capability", async () => {
    mocks.capabilities = { account_devices: true, watch_summary: false };
    renderTab();
    const history = screen.getByRole("region", { name: "Watch history" });
    await within(history).findByText("Dune: Part Two");
    expect(within(history).getByText("Showing 1")).toBeInTheDocument();
    expect(requests.some((url) => url.pathname.endsWith("/watch-summary"))).toBe(false);
  });
});

describe("ActivityTab devices and addresses", () => {
  it("links saved settings to the device in Preferences and hides older devices", async () => {
    const u = userEvent.setup();
    renderTab();
    const devices = screen.getByRole("region", { name: "Devices" });
    await within(devices).findByText("Apple TV 4K");
    expect(within(devices).queryByText("Old iPad")).not.toBeInTheDocument();
    expect(within(devices).getByRole("link", { name: "2 saved settings" })).toHaveAttribute(
      "href",
      "/admin/users/7?tab=preferences&level=device.p1.dev-tv",
    );
    await u.click(within(devices).getByRole("button", { name: "Show all 2" }));
    expect(within(devices).getByText("Old iPad")).toBeInTheDocument();
  });

  it("hides the Devices card without the capability", async () => {
    mocks.capabilities = { account_devices: false, watch_summary: true };
    renderTab();
    await screen.findByText("192.168.1.40");
    expect(screen.queryByRole("region", { name: "Devices" })).not.toBeInTheDocument();
    expect(requests.some((url) => url.pathname.endsWith("/devices"))).toBe(false);
  });

  it("labels addresses local or remote and flags a new remote address", async () => {
    renderTab();
    const ips = screen.getByRole("region", { name: "IP addresses" });
    const local = (await within(ips).findByText("192.168.1.40")).closest("li")!;
    expect(local).toHaveTextContent("Local");
    expect(local).toHaveTextContent("8,214 requests");
    const fresh = within(ips).getByText("81.12.44.190").closest("li")!;
    expect(fresh).toHaveTextContent("First seen Sep 25");
    // First seen weeks ago: a known remote address, not a new one.
    const old = within(ips).getByText("2a02:c7c:4d1::12").closest("li")!;
    expect(old).toHaveTextContent("Remote");
    expect(old).not.toHaveTextContent("First seen");
  });
});
