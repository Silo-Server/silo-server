// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import type { AdminUser } from "@/api/types";

import { DownloadsTab } from "./DownloadsTab";

const mocks = vi.hoisted(() => ({
  capabilities: { account_downloads: true, account_devices: true } as Record<string, boolean>,
}));

vi.mock("@/hooks/queries/admin/users", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/admin/users")>()),
  useAdminUserCapabilities: () => ({
    data: { available: true, ...mocks.capabilities },
    isLoading: false,
  }),
}));

const user = { id: 7, username: "taylor" } as AdminUser;
const NOW = "2026-09-29T10:00:00.000Z";

function row(id: string, overrides: Record<string, unknown>) {
  return {
    id,
    profile_id: "p1",
    device_id: "iphone",
    content_id: `movie-${id}`,
    title: `Movie ${id}`,
    media_type: "movie",
    episode: null,
    status: "completed",
    quality: "original",
    effective_quality: "original",
    delivery_format: "original",
    target_bitrate_kbps: 0,
    file_size: 1_000_000_000,
    created_at: NOW,
    updated_at: NOW,
    completed_at: null,
    status_event_at: null,
    ...overrides,
  };
}

const downloads = [
  row("1", { title: "Dune: Part Two" }),
  row("2", { title: "Oppenheimer", status: "ready" }),
  row("3", {
    title: "The Bear",
    content_id: "bear",
    episode_id: "bear-1",
    media_type: "series",
    episode: { season_number: 3, episode_number: 1, title: "Tomorrow" },
  }),
  row("4", {
    title: "The Bear",
    content_id: "bear",
    episode_id: "bear-2",
    media_type: "series",
    status: "downloading",
    episode: { season_number: 3, episode_number: 2, title: "Next" },
  }),
  row("5", { title: "Inside Out 2", device_id: "pixel", profile_id: "p2", status: "ready" }),
  row("6", { title: "Failed Movie", status: "failed" }),
];

function json(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}

function route(url: URL): unknown {
  const done = { has_more: false };
  switch (url.pathname) {
    case "/api/v2/admin/users/7/downloads":
      return { items: downloads, page: done };
    case "/api/v2/admin/users/7/download-subscriptions":
      return {
        items: [
          {
            id: "m1",
            profile_id: "p1",
            device_id: "iphone",
            series_id: "bear",
            series_title: "The Bear",
            mode: "latest_season",
            season_numbers: [],
            target_season: 3,
            delete_watched: false,
            max_storage_bytes: 10 * 1024 ** 3,
            active: true,
            on_device: 1,
            in_progress: 1,
            removed_episodes: 0,
            created_at: NOW,
            updated_at: NOW,
          },
        ],
        page: done,
      };
    case "/api/v2/admin/users/7/devices":
      return {
        items: [
          {
            device_id: "iphone",
            device_name: "iPhone 17",
            device_platform: "iOS",
            last_seen_at: NOW,
            last_updated: NOW,
            override_count: 0,
            profiles: [],
          },
          {
            device_id: "pixel",
            device_name: "Pixel 9 Pro",
            device_platform: "Android",
            last_seen_at: NOW,
            last_updated: NOW,
            override_count: 0,
            profiles: [],
          },
        ],
        page: done,
      };
    case "/api/v2/admin/users/7/profiles":
      return {
        items: [
          { id: "p1", name: "Main", last_seen_at: null },
          { id: "p2", name: "Kids", last_seen_at: null },
        ],
        page: done,
      };
    default:
      throw new Error(`unexpected request ${url.pathname}`);
  }
}

let requests: URL[] = [];

class MockResizeObserver implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function renderTab() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/admin/users/7?tab=downloads"]}>
        <DownloadsTab user={user} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function deviceCard(name: string) {
  return screen.getByRole("heading", { name }).closest("section") as HTMLElement;
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  mocks.capabilities = { account_downloads: true, account_devices: true };
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
  vi.unstubAllGlobals();
});

describe("DownloadsTab", () => {
  it("filters by device, profile and status", async () => {
    const u = userEvent.setup();
    renderTab();
    await screen.findByRole("heading", { name: "iPhone 17" });

    await u.click(screen.getByRole("tab", { name: "Pixel 9 Pro" }));
    expect(screen.queryByRole("heading", { name: "iPhone 17" })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Pixel 9 Pro" })).toBeInTheDocument();
    // The device tab narrows the monitored series too.
    expect(screen.getByText("No monitored series match these filters.")).toBeInTheDocument();

    await u.click(screen.getByRole("tab", { name: "All devices" }));
    await u.click(screen.getByRole("combobox", { name: "Profile" }));
    await u.click(await screen.findByRole("option", { name: "Main" }));
    expect(screen.queryByRole("heading", { name: "Pixel 9 Pro" })).not.toBeInTheDocument();

    // In progress counts what the tile counts: the Android request is not in it.
    await u.click(screen.getByRole("tab", { name: "All devices" }));
    await u.click(screen.getByRole("combobox", { name: "Profile" }));
    await u.click(await screen.findByRole("option", { name: "All profiles" }));
    await u.click(screen.getByRole("combobox", { name: "Status" }));
    await u.click(await screen.findByRole("option", { name: "In progress" }));
    expect(screen.queryByRole("heading", { name: "Pixel 9 Pro" })).not.toBeInTheDocument();
    await u.click(screen.getByRole("combobox", { name: "Status" }));
    await u.click(await screen.findByRole("option", { name: "Requested on Android" }));
    expect(screen.queryByRole("heading", { name: "iPhone 17" })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Pixel 9 Pro" })).toBeInTheDocument();

    await u.click(screen.getByRole("combobox", { name: "Profile" }));
    await u.click(await screen.findByRole("option", { name: "Main" }));
    await u.click(screen.getByRole("combobox", { name: "Status" }));
    await u.click(await screen.findByRole("option", { name: "Failed" }));
    const iphone = deviceCard("iPhone 17");
    expect(within(iphone).getByText("Failed Movie")).toBeInTheDocument();
    expect(within(iphone).queryByText("Dune: Part Two")).not.toBeInTheDocument();
  });

  it("shows one notice when the server can't list account downloads", () => {
    mocks.capabilities = { account_downloads: false, account_devices: true };
    renderTab();
    expect(screen.getByText("Downloads aren't available on this server.")).toBeInTheDocument();
    expect(requests.some((url) => /download|devices/.test(url.pathname))).toBe(false);
  });
});
