import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Collections from "./Collections";

const capability = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/queries/collections", () => ({
  useCollectionCapabilities: capability,
  useCollections: () => ({ data: [], isLoading: false }),
  useServerCollections: () => ({ data: [] }),
  useDeleteCollection: () => ({}),
  useReorderCollections: () => ({}),
  useSetCollectionShared: () => ({}),
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({ useCurrentProfile: () => ({ profile: null }) }));
vi.mock("@/hooks/queries/userCollectionImports", () => ({ useSyncUserCollection: () => ({}) }));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium" } }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));

function Where() {
  const location = useLocation();
  return <p data-testid="location">{`${location.pathname}${location.search}`}</p>;
}

function show(path = "/collections") {
  render(
    // The picker also asks for the admin capabilities, disabled for a profile.
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/collections" element={<Collections />} />
          <Route path="*" element={null} />
        </Routes>
        <Where />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function location() {
  return screen.getByTestId("location");
}

beforeEach(() => {
  vi.clearAllMocks();
  capability.mockReturnValue({
    data: { imports: true, artwork: true, item_reorder: true, import_sources: ["mdblist"] },
  });
});
afterEach(cleanup);

describe("New collection on the Collections page", () => {
  it("opens the type picker from the header and from the empty state", async () => {
    show();
    const user = userEvent.setup();
    const [header, empty] = screen.getAllByRole("button", { name: "New collection" });
    await user.click(header!);
    const dialog = await screen.findByRole("dialog", { name: "New collection" });
    expect(location()).toHaveTextContent("/collections?dialog=new");
    expect(within(dialog).getByRole("link", { name: "Manual" })).toHaveAttribute(
      "href",
      "/collections/new?type=manual",
    );
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(location()).toHaveTextContent(/^\/collections$/);

    await user.click(empty!);
    expect(await screen.findByRole("dialog", { name: "New collection" })).toBeInTheDocument();
  });
});
