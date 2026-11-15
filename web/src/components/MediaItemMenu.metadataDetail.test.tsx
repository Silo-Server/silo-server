import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";
import { fetchCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { catalogKeys } from "@/hooks/queries/keys";
import { MetadataActionDialogHost } from "./MediaItemMenu";

const mocks = vi.hoisted(() => ({ v2: vi.fn(), editItem: vi.fn() }));

vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: mocks.v2 };
});

vi.mock("@/api/v2/catalog", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/catalog")>("@/api/v2/catalog");
  return { ...actual, catalogItemDetailFromV2: (detail: ItemDetail) => detail };
});

vi.mock("@/components/EditMetadataDialog", () => ({
  default: ({ item }: { item: ItemDetail }) => {
    mocks.editItem(item);
    return <div>Edit {item.locked_fields?.join(",") || "none"}</div>;
  },
}));

afterEach(() => {
  vi.resetAllMocks();
});

function movie(locked_fields: number[]) {
  return { content_id: "movie-1", type: "movie", title: "Silo", locked_fields } as ItemDetail;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => (resolve = res));
  return { promise, resolve };
}

function renderHost(seed?: ItemDetail, prepare?: (queryClient: QueryClient) => void) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { staleTime: 2 * 60_000, retry: false } },
  });
  if (seed) queryClient.setQueryData(catalogKeys.itemDetail("movie-1"), seed);
  prepare?.(queryClient);
  render(
    <QueryClientProvider client={queryClient}>
      <MetadataActionDialogHost action="edit" contentId="movie-1" onClose={() => undefined} />
    </QueryClientProvider>,
  );
  return queryClient;
}

// The cached item can look fresh yet predate a write made while no dialog was open.
it("opens Edit Metadata on a fresh read of the item, not the cached one", async () => {
  const read = deferred<ItemDetail>();
  mocks.v2.mockReturnValueOnce(read.promise);

  const queryClient = renderHost(movie([]));
  expect(await screen.findByText("Loading the latest item details…")).toBeInTheDocument();
  // A cache update, such as a favorite toggle, isn't a read. Query
  // notifications go out on a zero-delay timer.
  await act(async () => {
    queryClient.setQueryData(catalogKeys.itemDetail("movie-1"), movie([]));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

  expect(screen.getByText("Loading the latest item details…")).toBeInTheDocument();
  expect(mocks.editItem).not.toHaveBeenCalled();

  await act(async () => read.resolve(movie([10])));

  expect(await screen.findByText("Edit 10")).toBeInTheDocument();
});

// A save from an earlier opening invalidates the item when it lands, and stays
// pending until the item is refetched; the dialog must wait for that refetch.
it("opens Edit Metadata on the read after an invalidation, not the one before", async () => {
  const before = deferred<ItemDetail>();
  const after = deferred<ItemDetail>();
  mocks.v2.mockReturnValueOnce(before.promise).mockReturnValueOnce(after.promise);

  const queryClient = renderHost(movie([]));
  expect(await screen.findByText("Loading the latest item details…")).toBeInTheDocument();
  await act(async () => {
    void queryClient.invalidateQueries({ queryKey: catalogKeys.itemDetail("movie-1") });
    before.resolve(movie([]));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

  expect(screen.getByText("Loading the latest item details…")).toBeInTheDocument();
  expect(mocks.editItem).not.toHaveBeenCalled();

  await act(async () => after.resolve(movie([10])));

  expect(await screen.findByText("Edit 10")).toBeInTheDocument();
});

// A read already running when the dialog opens, such as a hover prefetch, may
// have started before a write that has since landed.
it("opens Edit Metadata on a read it started, not one already running", async () => {
  const earlier = deferred<ItemDetail>();
  const own = deferred<ItemDetail>();
  mocks.v2.mockReturnValueOnce(earlier.promise).mockReturnValueOnce(own.promise);

  renderHost(movie([]), (queryClient) => {
    void queryClient.prefetchQuery({
      queryKey: catalogKeys.itemDetail("movie-1"),
      queryFn: () => fetchCatalogItemDetail("movie-1"),
      staleTime: 0,
    });
  });
  expect(await screen.findByText("Loading the latest item details…")).toBeInTheDocument();
  await act(async () => {
    earlier.resolve(movie([]));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

  expect(screen.getByText("Loading the latest item details…")).toBeInTheDocument();
  expect(mocks.editItem).not.toHaveBeenCalled();

  await act(async () => own.resolve(movie([10])));

  expect(await screen.findByText("Edit 10")).toBeInTheDocument();
});

it("keeps an open Edit Metadata when a later refetch fails", async () => {
  mocks.v2.mockResolvedValueOnce(movie([10]));

  const queryClient = renderHost();
  expect(await screen.findByText("Edit 10")).toBeInTheDocument();

  mocks.v2.mockRejectedValueOnce(new Error("offline"));
  await act(() => queryClient.invalidateQueries({ queryKey: catalogKeys.itemDetail("movie-1") }));

  expect(screen.getByText("Edit 10")).toBeInTheDocument();
});
