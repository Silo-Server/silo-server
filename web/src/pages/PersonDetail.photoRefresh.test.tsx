import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, expect, it, vi } from "vitest";

import { adminRefreshPerson, getPerson, refreshPerson } from "@/api/v2/people";
import { v2Fixture } from "@/api/v2/testing";
import { catalogKeys, personKeys } from "@/hooks/queries/keys";
import { useAuth } from "@/hooks/useAuth";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import PersonDetail from "./PersonDetail";

vi.mock("@/api/v2/people", () => ({
  getPerson: vi.fn(),
  refreshPerson: vi.fn(),
  adminRefreshPerson: vi.fn(),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: vi.fn(() => ({ user: null })) }));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: vi.fn(() => false) }));
vi.mock("@/hooks/queries/catalog", () => ({ useCatalogWindow: () => ({ isLoading: false }) }));
vi.mock("@/components/ItemGrid", () => ({ default: () => null }));

const clients: QueryClient[] = [];
afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
  vi.useRealTimers();
  vi.clearAllMocks();
  vi.mocked(useIsActingAdmin).mockReturnValue(false);
});

it("renders a prefetched person at once and still reads it as a view", async () => {
  const id = "7";
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 120_000 } },
  });
  clients.push(client);
  const person = {
    id,
    name: "Prefetched Actor",
    bio: "Cached biography",
    birth_date: "1940-04-25",
  };
  client.setQueryData(personKeys.detail(id), person);
  vi.mocked(getPerson).mockResolvedValue(person);
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/person/${id}`]}>
        <Routes>
          <Route path="/person/:id" element={<PersonDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(screen.getByText("Cached biography")).toBeInTheDocument();
  await waitFor(() => expect(getPerson).toHaveBeenCalled());
  // A view read omits `prefetch`, so the server may queue a due refresh.
  expect(vi.mocked(getPerson).mock.calls[0]?.[1]).not.toHaveProperty("prefetch");
});

it("refreshes cached cast after a person read observes a background photo update", async () => {
  const id = "9007199254740993";
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 120_000 } },
  });
  clients.push(client);
  const person = {
    id: id,
    name: "Actor",
    bio: "Biography",
    birth_date: "1979-08-01",
    photo_url: "https://images.example.test/old.jpg",
  };
  const itemKey = catalogKeys.itemDetail("movie", 12);
  const item = {
    content_id: "movie",
    cast: [{ person_id: id, photo_url: person.photo_url }],
    crew: [],
  };
  client.setQueryData(personKeys.detail(id), person);
  vi.mocked(getPerson).mockResolvedValue(person);
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/person/${id}`]}>
        <Routes>
          <Route path="/person/:id" element={<PersonDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  expect(screen.getByRole("img", { name: "Actor" })).toHaveAttribute("src", person.photo_url);

  // The cached item is fresh when a subsequent poll sees the completed refresh.
  client.setQueryData(itemKey, item);
  const updated = { ...person, photo_url: "https://images.example.test/new.jpg" };
  vi.mocked(getPerson).mockResolvedValue(updated);
  await act(() => client.refetchQueries({ queryKey: personKeys.detail(id) }));

  await waitFor(() => {
    expect(screen.getByRole("img", { name: "Actor" })).toHaveAttribute("src", updated.photo_url);
    expect(client.getQueryState(itemKey)?.isInvalidated).toBe(true);
  });
});

it.each([
  { queueDelay: 0, rotateSignature: false },
  { queueDelay: 33_000, rotateSignature: false },
  { queueDelay: 123_000, rotateSignature: false },
  { queueDelay: 660_000, rotateSignature: false },
  { queueDelay: 33_000, rotateSignature: true },
  { queueDelay: 33_000, rotateSignature: false, coldNavigation: "prefetch" },
  { queueDelay: 33_000, rotateSignature: false, coldNavigation: "mount" },
  { queueDelay: 33_000, rotateSignature: false, isAdmin: true },
  { queueDelay: 33_000, rotateSignature: false, coldNavigation: "mount", isAdmin: true },
  { queueDelay: 33_000, rotateSignature: false, automatic: true },
  { queueDelay: 33_000, rotateSignature: false, coldNavigation: "mount", automatic: true },
  {
    queueDelay: 0,
    rotateSignature: false,
    coldNavigation: "prefetch",
    completeBeforeItemLoads: true,
  },
  {
    queueDelay: 0,
    rotateSignature: false,
    coldNavigation: "prefetch",
    completeBeforeItemLoads: true,
    stayOnPersonPage: true,
    isAdmin: true,
  },
  {
    queueDelay: 0,
    rotateSignature: false,
    coldNavigation: "prefetch",
    completeBeforeItemLoads: true,
    stayOnPersonPage: true,
    isAdmin: true,
    repeatRefresh: true,
  },
])(
  "observes a photo refresh after $queueDelay ms with signature rotation=$rotateSignature, cold navigation=$coldNavigation, early completion=$completeBeforeItemLoads, admin=$isAdmin, automatic=$automatic, person page open=$stayOnPersonPage, and repeat=$repeatRefresh",
  async ({
    queueDelay,
    rotateSignature,
    coldNavigation,
    completeBeforeItemLoads,
    isAdmin,
    automatic,
    stayOnPersonPage,
    repeatRefresh,
  }) => {
    vi.useFakeTimers();
    vi.mocked(useAuth).mockReturnValue({ user: { id: 1 } } as ReturnType<typeof useAuth>);
    vi.mocked(useIsActingAdmin).mockReturnValue(isAdmin ?? false);
    const id = "9007199254740993";
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 120_000 } },
    });
    clients.push(client);
    const person = {
      id: id,
      name: "Actor",
      bio: "Biography",
      birth_date: "1979-08-01",
      photo_url: "https://images.example.test/old.jpg",
    };
    const itemKey = catalogKeys.itemDetail("movie", 12);
    const item = {
      content_id: "movie",
      cast: [{ person_id: id, photo_url: person.photo_url }],
      crew: [],
    };
    client.setQueryData(personKeys.detail(id), person);
    if (!coldNavigation) client.setQueryData(itemKey, item);
    vi.mocked(getPerson).mockResolvedValue(person);
    vi.mocked(adminRefreshPerson).mockResolvedValue(person);
    vi.mocked(refreshPerson).mockResolvedValue(
      v2Fixture<"POST /api/v2/catalog/people/{id}/refresh">({ status: "queued", person_id: id }),
    );
    const view = render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[`/person/${id}`]}>
          <Routes>
            <Route path="/person/:id" element={<PersonDetail />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await act(async () => {
      if (!automatic) {
        fireEvent.click(
          screen.getByRole("button", { name: isAdmin ? "Refresh now" : "Refresh metadata" }),
        );
      }
      await vi.advanceTimersByTimeAsync(0);
    });
    if (automatic) {
      expect(refreshPerson).not.toHaveBeenCalled();
      expect(adminRefreshPerson).not.toHaveBeenCalled();
    } else expect(isAdmin ? adminRefreshPerson : refreshPerson).toHaveBeenCalledWith(id);

    let serverItem = item;
    const photoUrl = "https://images.example.test/new.jpg";
    const readItem = vi.fn(async () => serverItem);
    let finishItem: (() => void) | undefined;
    if (coldNavigation) {
      const firstRead = new Promise<typeof item>((resolve) => {
        finishItem = () => resolve(item);
      });
      readItem.mockImplementationOnce(() => firstRead);
      if (coldNavigation === "prefetch") {
        void client.prefetchQuery({ queryKey: itemKey, queryFn: readItem });
      }
    }
    if (completeBeforeItemLoads) {
      serverItem = { ...item, cast: [{ person_id: id, photo_url: photoUrl }] };
      vi.mocked(getPerson).mockResolvedValue({ ...person, photo_url: photoUrl });
      await act(() => client.refetchQueries({ queryKey: personKeys.detail(id) }));
    }
    if (repeatRefresh) {
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Refresh now" }));
        await vi.advanceTimersByTimeAsync(0);
      });
    }
    if (!stayOnPersonPage) view.unmount();

    const returned = renderHook(() => useQuery({ queryKey: itemKey, queryFn: readItem }), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    });
    await act(async () => {
      finishItem?.();
      await vi.advanceTimersByTimeAsync(2);
    });
    if (completeBeforeItemLoads) {
      expect(returned.result.current.data?.cast[0]?.photo_url).toBe(photoUrl);
      return;
    }
    expect(returned.result.current.data?.cast[0]?.photo_url).toBe(person.photo_url);

    if (rotateSignature) {
      vi.mocked(getPerson).mockResolvedValue({
        ...person,
        photo_url: `${person.photo_url}?X-Amz-Signature=rotated`,
      });
    }

    await act(() => vi.advanceTimersByTimeAsync(queueDelay));
    expect(returned.result.current.data?.cast[0]?.photo_url).toBe(person.photo_url);

    serverItem = { ...item, cast: [{ person_id: id, photo_url: photoUrl }] };
    vi.mocked(getPerson).mockResolvedValue({ ...person, photo_url: photoUrl });
    await act(() => vi.advanceTimersByTimeAsync(queueDelay === 0 ? 3_001 : 30_001));

    expect(returned.result.current.data?.cast[0]?.photo_url).toBe(photoUrl);
  },
);
