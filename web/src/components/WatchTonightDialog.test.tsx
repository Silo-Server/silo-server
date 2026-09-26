// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import fixture from "../../../contracts/api/v2/fixtures/list_watch_tonight_cards_ok.json";
import WatchTonightDialog from "./WatchTonightDialog";

vi.mock("./watchtonight/CardStack", () => ({
  default: ({
    cards,
    onNeedMore,
    onReset,
  }: {
    cards: unknown[];
    onNeedMore: () => void;
    onReset: () => void;
  }) => (
    <div>
      <span>Cards: {cards.length}</span>
      <button onClick={onNeedMore}>More</button>
      <button onClick={onReset}>Start Over</button>
    </div>
  ),
}));
afterEach(() => vi.unstubAllGlobals());
it("starts over with empty exclusions instead of reusing a finished swipe session", async () => {
  installPolicyStorageMocks();
  setProfileId("p-owner");
  const exclusions: string[][] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const ids = new URL(String(input), "http://localhost").searchParams.getAll("exclude_ids");
      exclusions.push(ids);
      return jsonResponse({
        ...fixture,
        items: [{ ...fixture.items[0], content_id: `movie:${ids.length}` }],
        has_more: true,
        paging_limited: ids.length > 0,
      });
    }),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <WatchTonightDialog open onOpenChange={vi.fn()} />
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: /Pick Up Where I Left Off/ }));
  await screen.findByText("Cards: 1");
  fireEvent.click(screen.getByRole("button", { name: "More" }));
  await screen.findByText("Cards: 2");
  fireEvent.click(screen.getByRole("button", { name: "Start Over" }));
  fireEvent.click(screen.getByRole("button", { name: /Pick Up Where I Left Off/ }));
  await waitFor(() => expect(exclusions).toHaveLength(3));
  expect(exclusions).toEqual([[], ["movie:0"], []]);
  await screen.findByText("Cards: 1");
});
