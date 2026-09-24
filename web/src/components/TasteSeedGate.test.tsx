// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import listFavoritesOk from "../../../contracts/api/v2/fixtures/list_favorites_ok.json";

import { setProfileId } from "@/api/client";
import { invalidateMediaSurfaceQueries } from "@/hooks/queries/mediaSurfaceRefresh";
import { setTasteSeedDismissed } from "@/lib/tasteSeed";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import TasteSeedBanner from "./TasteSeedBanner";
import TasteSeedGate from "./TasteSeedGate";

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ profile: { id: "p-owner" } }),
}));

vi.mock("@/hooks/queries/onboarding", () => ({
  useOnboardingState: () => ({ data: { done: true } }),
}));

// The server default page size when a request carries no limit.
const DEFAULT_FAVORITES_LIMIT = 50;

type FavoritesBody = { items: unknown[]; page?: { next_cursor: string; has_more: boolean } };

const noFavorites: FavoritesBody = { items: [] };
// The newest favorite is one the viewer may not see: the page carries no
// card, but a cursor says more favorites follow.
const hiddenNewestFavorite: FavoritesBody = {
  items: [],
  page: { next_cursor: "cursor-1", has_more: true },
};

/** Stubs fetch and records the limit of every favorites list request. */
function stubFavorites(body: FavoritesBody) {
  const limits: number[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname !== "/api/v2/favorites") {
        return new Response(null, { status: 404 });
      }
      const limit = url.searchParams.get("limit");
      limits.push(limit === null ? DEFAULT_FAVORITES_LIMIT : Number(limit));
      return jsonResponse(body);
    }),
  );
  return limits;
}

/** Mounts the Home route the way App does: the gate wrapping Home's banner. */
function renderHome() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/"]}>
        <Routes>
          <Route
            path="/"
            element={
              <TasteSeedGate>
                <div>home</div>
                <TasteSeedBanner />
              </TasteSeedGate>
            }
          />
          <Route path="/taste-seed" element={<div>taste seed</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}

describe("taste-seed gate and banner", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("asks the favorites endpoint for one card per Home mount", async () => {
    const limits = stubFavorites(listFavoritesOk);

    const client = renderHome();
    await waitFor(() => expect(client.isFetching()).toBe(0));
    await screen.findByText("home");

    // The gate and the banner only need to know whether any favorite exists.
    expect(limits).toEqual([1]);
  });

  it("refetches the one-card page when a media-surface refresh lands", async () => {
    const limits = stubFavorites(listFavoritesOk);

    const client = renderHome();
    await waitFor(() => expect(limits).toHaveLength(1));

    // Favorite toggles, realtime user_state events, and catalog sweeps all
    // refresh the favorites prefix; the gate must still hear about them.
    await invalidateMediaSurfaceQueries(client);
    await waitFor(() => expect(client.isFetching()).toBe(0));

    expect(limits).toEqual([1, 1]);
  });

  it("redirects a profile with no favorites to the taste-seed picker", async () => {
    stubFavorites(noFavorites);

    renderHome();

    await screen.findByText("taste seed");
  });

  it("keeps Home when the newest favorite is hidden but more follow", async () => {
    stubFavorites(hiddenNewestFavorite);

    const client = renderHome();
    await waitFor(() => expect(client.isFetching()).toBe(0));

    expect(screen.getByText("home")).toBeTruthy();
    expect(screen.queryByText("taste seed")).toBeNull();
  });

  it("shows the banner only to a profile that skipped the picker and has no favorites", async () => {
    setTasteSeedDismissed("p-owner");
    stubFavorites(noFavorites);

    renderHome();

    await screen.findByText("Personalize your home");
  });

  it("hides the banner once the profile favorites something", async () => {
    setTasteSeedDismissed("p-owner");
    stubFavorites(noFavorites);

    const client = renderHome();
    await screen.findByText("Personalize your home");

    // The profile favorites an item; the toggle's media-surface refresh
    // reaches the banner, which then has its answer and stays away.
    stubFavorites(listFavoritesOk);
    await invalidateMediaSurfaceQueries(client);
    await waitFor(() => expect(client.isFetching()).toBe(0));

    expect(screen.getByText("home")).toBeTruthy();
    expect(screen.queryByText("Personalize your home")).toBeNull();
  });

  it("hides the banner when the newest favorite is hidden but more follow", async () => {
    setTasteSeedDismissed("p-owner");
    stubFavorites(hiddenNewestFavorite);

    const client = renderHome();
    await waitFor(() => expect(client.isFetching()).toBe(0));

    expect(screen.getByText("home")).toBeTruthy();
    expect(screen.queryByText("Personalize your home")).toBeNull();
  });
});
