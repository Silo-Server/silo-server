import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { CuratedTitlesEditor } from "./CuratedTitlesEditor";

vi.mock("@/hooks/queries/items", () => ({
  fetchWatchDetail: async (id: string) => ({ title: `Title ${id}`, year: 2001 }),
}));
vi.mock("@/hooks/queries/catalog", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/catalog")>()),
  fetchCatalogPage: async () => ({
    items: [
      { content_id: "m1", title: "Already here", year: 2001, type: "movie" },
      { content_id: "m9", title: "Paddington", year: 2014, type: "movie" },
    ],
  }),
}));

let latest: string[];

function Harness({ initial }: { initial: string[] }) {
  const [ids, setIds] = useState(initial);
  return (
    <QueryClientProvider client={new QueryClient()}>
      <CuratedTitlesEditor
        itemIds={ids}
        onChange={(next) => {
          latest = next;
          setIds(next);
        }}
      />
    </QueryClientProvider>
  );
}

describe("CuratedTitlesEditor", () => {
  it("names saved titles, and moves and removes them", async () => {
    render(<Harness initial={["m1", "m2"]} />);
    expect(await screen.findByText("Title m1 (2001)")).toBeInTheDocument();
    expect(screen.getByText("2 titles, shown in this order")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move Title m1 (2001) up" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Move Title m1 (2001) down" }));
    expect(latest).toEqual(["m2", "m1"]);
    await userEvent.click(screen.getByRole("button", { name: "Remove Title m2 (2001)" }));
    expect(latest).toEqual(["m1"]);
  });

  it("adds titles from a search, once each", async () => {
    render(<Harness initial={["m1"]} />);
    await userEvent.type(screen.getByRole("searchbox", { name: "Search titles to add" }), "pad");
    await userEvent.click(await screen.findByRole("button", { name: "Add Paddington" }));
    expect(latest).toEqual(["m1", "m9"]);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Paddington is added" })).toBeDisabled(),
    );
    expect(screen.getByRole("button", { name: "Already here is added" })).toBeDisabled();
  });

  it("asks for at least one title when the list is empty", () => {
    render(<Harness initial={[]} />);
    expect(screen.getByText("Search above and add at least one title.")).toBeInTheDocument();
  });
});
