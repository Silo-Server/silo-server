import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AddToCollectionDialog from "./AddToCollectionDialog";

const mocks = vi.hoisted(() => ({ mutate: vi.fn(), isAdmin: false }));

vi.mock("@/hooks/queries/collections", () => ({
  useCollections: () => ({
    data: [{ id: "user-1", name: "To Watch", collection_type: "manual" }],
    isLoading: false,
  }),
  useAddItemToCollection: () => ({ mutate: mocks.mutate, isPending: false }),
}));
vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: () => ({ data: [{ id: 2, name: "TV Shows" }] }),
}));
vi.mock("@/hooks/queries/libraryCollections", () => ({
  libraryCollectionsQueryOptions: (id: number) => ({ queryKey: ["library-collections", id] }),
  getLibraryCollectionList: (data: unknown) => data,
}));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQueries: () => [
    { data: [{ id: "lib-1", title: "Best of TV", collection_type: "manual" }], isLoading: false },
  ],
}));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => mocks.isAdmin }));

function show(props: Partial<Parameters<typeof AddToCollectionDialog>[0]> = {}) {
  render(
    <AddToCollectionDialog
      open
      onOpenChange={() => {}}
      mediaItemId="season-1"
      itemTitle="Alpha — Season 1"
      {...props}
    />,
  );
}

async function addTo(name: string) {
  await userEvent.click(screen.getByRole("button", { name: new RegExp(name) }));
  await userEvent.click(screen.getByRole("button", { name: "Add" }));
}

describe("AddToCollectionDialog", () => {
  beforeEach(() => {
    mocks.mutate.mockReset();
    mocks.isAdmin = false;
  });

  it("adds the item directly when there is no series option", async () => {
    show({ mediaItemId: "movie-1", itemTitle: "Heat" });
    expect(screen.queryByRole("group", { name: "What to add" })).toBeNull();
    await addTo("To Watch");
    expect(mocks.mutate).toHaveBeenCalledWith(
      { collectionId: "user-1", mediaItemId: "movie-1", source: "user" },
      expect.any(Object),
    );
  });

  it("adds the season by default and the series when chosen", async () => {
    show({ seriesOption: { id: "series-1", title: "Alpha" } });
    expect(screen.getByRole("button", { name: /This season/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await addTo("To Watch");
    expect(mocks.mutate).toHaveBeenLastCalledWith(
      expect.objectContaining({ mediaItemId: "season-1" }),
      expect.any(Object),
    );
    await userEvent.click(screen.getByRole("button", { name: /Whole series/ }));
    expect(screen.getByText(/add "Alpha" to/)).toBeTruthy();
    await addTo("To Watch");
    expect(mocks.mutate).toHaveBeenLastCalledWith(
      expect.objectContaining({ mediaItemId: "series-1" }),
      expect.any(Object),
    );
  });

  it("disables library collections for a season, not for the series", async () => {
    mocks.isAdmin = true;
    show({ seriesOption: { id: "series-1", title: "Alpha" } });
    expect(screen.getByRole("button", { name: /Best of TV/ })).toBeDisabled();
    expect(screen.getByText("Library collections hold whole series")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Whole series/ }));
    expect(screen.getByRole("button", { name: /Best of TV/ })).toBeEnabled();
    await addTo("Best of TV");
    expect(mocks.mutate).toHaveBeenLastCalledWith(
      { collectionId: "lib-1", mediaItemId: "series-1", source: "library" },
      expect.any(Object),
    );
  });
});
