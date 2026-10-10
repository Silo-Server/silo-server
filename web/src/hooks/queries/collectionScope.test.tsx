import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";

import type { QueryDefinition } from "@/api/types";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getCollectionOk from "../../../../contracts/api/v2/fixtures/get_collection_ok.json";
import {
  PERSONAL_SCOPE,
  SERVER_SCOPE,
  type CollectionDraft,
  type CollectionScope,
} from "@/lib/collections/scope";
import { adminCollectionList, adminSmartCollection } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import { collectionFromV2 } from "@/api/personalCollections";
import {
  useCollectionDraft,
  useListedPoster,
  useScopeEditor,
  useScopePreview,
} from "./collectionScope";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());

installV2Recorder();

const ETAG = '"/api/v2/collections/c1#1"';
let client: QueryClient;

function open(
  id: string | undefined,
  scope: CollectionScope = PERSONAL_SCOPE,
  existing = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  client = existing;
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return renderHook(({ current }) => useScopeEditor(scope, current), {
    wrapper,
    initialProps: { current: id },
  });
}

function listing(posterUrl: string) {
  return { items: [{ ...getCollectionOk, poster_url: posterUrl }] };
}

beforeEach(() => {
  v2Recorder.answer("GET /api/v2/collections", listing("https://images.example/listed.png"));
});

describe("useScopeEditor (personal)", () => {
  it("opens the collection with its ETag and the list's poster", async () => {
    let releaseSnapshot!: () => void;
    const listed = new Promise<void>((resolve) => (releaseSnapshot = resolve));
    v2Recorder.answer("GET /api/v2/collections/{id}", () => listed.then(() => getCollectionOk));
    const { result } = open("c1");
    await waitFor(() => expect(client.getQueryData(PERSONAL_SCOPE.keys.list)).toBeDefined());
    releaseSnapshot();
    await waitFor(() => expect(result.current.snapshot).toBeDefined());
    expect(result.current.snapshot!.etag).toBe(ETAG);
    expect(result.current.snapshot!.view).toMatchObject({
      id: "c1",
      name: "Rainy days",
      posterUrl: "https://images.example/listed.png",
      ownerProfileId: "p-owner",
    });
    expect(result.current.snapshot!.view.raw.poster_url).toBe("https://images.example/listed.png");
  });

  it("keeps the first snapshot through a background refetch", async () => {
    const { result } = open("c1");
    await waitFor(() => expect(result.current.snapshot).toBeDefined());
    v2Recorder.answer("GET /api/v2/collections/{id}", {
      ...getCollectionOk,
      name: "Renamed elsewhere",
    });
    v2Recorder.answer("GET /api/v2/collections", listing("https://images.example/new.png"));
    v2Recorder.bump("/api/v2/collections/c1");

    await act(() => client.invalidateQueries());
    await waitFor(() => expect(v2Recorder.callsOf("GET /api/v2/collections/{id}")).toHaveLength(2));

    expect(result.current.snapshot!.etag).toBe(ETAG);
    expect(result.current.snapshot!.view.name).toBe("Rainy days");
    expect(result.current.snapshot!.view.posterUrl).toBe("https://images.example/listed.png");
  });

  it("keeps the collection's own poster when the list could not be read", async () => {
    v2Recorder.answer("GET /api/v2/collections", () => {
      throw new Error("list unavailable");
    });
    v2Recorder.answer("GET /api/v2/collections/{id}", {
      ...getCollectionOk,
      poster_url: "https://images.example/own.png",
    });
    const { result } = open("c1");
    await waitFor(() => expect(result.current.snapshot).toBeDefined());
    expect(result.current.snapshot!.view.posterUrl).toBe("https://images.example/own.png");
  });

  it("opens without waiting for the list, as the personal editor always has", async () => {
    v2Recorder.answer("GET /api/v2/collections", () => new Promise(() => {}));
    v2Recorder.answer("GET /api/v2/collections/{id}", {
      ...getCollectionOk,
      poster_url: "https://images.example/own.png",
    });
    const { result } = open("c1");
    await waitFor(() => expect(result.current.snapshot).toBeDefined());
    expect(result.current.isLoading).toBe(false);
    expect(result.current.snapshot!.view.posterUrl).toBe("https://images.example/own.png");
  });

  it("reads no collection without an id, and is loading only until the list arrives", async () => {
    const { result } = open(undefined);
    expect(result.current.isLoading).toBe(true);
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.snapshot).toBeUndefined();
    expect(v2Recorder.callsOf("GET /api/v2/collections/{id}")).toEqual([]);
  });

  it("reads the collection again when it opens on a copy another page cached", async () => {
    // The collection page read it moments ago, well inside the app's staleTime.
    const cached = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 2 * 60_000 } },
    });
    await cached.fetchQuery({
      queryKey: PERSONAL_SCOPE.keys.snapshot("c1"),
      queryFn: () => PERSONAL_SCOPE.fetchSnapshot("c1"),
    });
    v2Recorder.answer("GET /api/v2/collections/{id}", {
      ...getCollectionOk,
      name: "Renamed elsewhere",
    });
    v2Recorder.bump("/api/v2/collections/c1");

    const { result } = open("c1", PERSONAL_SCOPE, cached);

    expect(result.current.snapshot).toBeUndefined();
    expect(result.current.isLoading).toBe(true);
    await waitFor(() => expect(result.current.snapshot).toBeDefined());
    expect(v2Recorder.callsOf("GET /api/v2/collections/{id}")).toHaveLength(2);
    expect(result.current.snapshot!.etag).toBe('"/api/v2/collections/c1#2"');
    expect(result.current.snapshot!.view.name).toBe("Renamed elsewhere");
    expect(result.current.isLoading).toBe(false);
  });

  it("drops the kept snapshot when the page moves to another collection", async () => {
    const { result, rerender } = open("c1");
    await waitFor(() => expect(result.current.snapshot).toBeDefined());
    v2Recorder.answer("GET /api/v2/collections/{id}", () => new Promise(() => {}));
    rerender({ current: "c2" });
    expect(result.current.snapshot).toBeUndefined();
    expect(result.current.isLoading).toBe(true);
  });
});

describe("useScopeEditor (server)", () => {
  it("waits for the list, since the admin read carries no artwork", async () => {
    let releaseList!: () => void;
    const listReady = new Promise<void>((resolve) => (releaseList = resolve));
    const smart = adminSmartCollection({});
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", smart);
    v2Recorder.answer("GET /api/v2/admin/collections", () =>
      listReady.then(() => adminCollectionList({ ...smart, poster_url: "listed.png" })),
    );
    const { result } = open("c1", SERVER_SCOPE as CollectionScope);
    await waitFor(() =>
      expect(client.getQueryData(SERVER_SCOPE.keys.snapshot("c1"))).toBeDefined(),
    );
    expect(result.current.snapshot).toBeUndefined();
    expect(result.current.isLoading).toBe(true);

    releaseList();
    await waitFor(() => expect(result.current.snapshot).toBeDefined());
    expect(result.current.isLoading).toBe(false);
    expect(result.current.snapshot!.view.posterUrl).toBe("listed.png");
  });
});

describe("useScopePreview", () => {
  const PREVIEW = "POST /api/v2/collections/preview";
  const rules = (minYear: number) =>
    ({
      library_ids: [],
      match: "all",
      groups: [{ match: "all", rules: [{ field: "year", op: "gte", value: minYear }] }],
    }) as unknown as QueryDefinition;
  const answerWith = (title: string) => ({
    items: [{ content_id: `movie:${title}`, title, type: "movie" }],
    page: { has_more: false },
    total: 1,
  });

  function preview(initial: QueryDefinition) {
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    return renderHook(
      ({ current }) => useScopePreview(PERSONAL_SCOPE as CollectionScope, current),
      { wrapper, initialProps: { current: initial } },
    );
  }

  it("asks once for a burst of rule changes, and keeps the last posters until it answers", async () => {
    v2Recorder.answer(PREVIEW, answerWith("Alien"));
    const { result, rerender } = preview(rules(1980));
    await waitFor(() => expect(result.current).toMatchObject({ status: "ready" }));
    expect(v2Recorder.callsOf(PREVIEW)).toHaveLength(1);

    let release!: () => void;
    const held = new Promise<void>((resolve) => (release = resolve));
    v2Recorder.answer(PREVIEW, () => held.then(() => answerWith("Heat")));
    rerender({ current: rules(1990) });
    rerender({ current: rules(1995) });
    rerender({ current: rules(2000) });
    expect(result.current).toMatchObject({
      status: "ready",
      refreshing: true,
      items: [{ title: "Alien" }],
    });

    await waitFor(() => expect(v2Recorder.callsOf(PREVIEW)).toHaveLength(2));
    expect(v2Recorder.callsOf(PREVIEW)[1]!.body).toMatchObject({
      query_definition: { groups: [{ rules: [{ value: 2000 }] }] },
    });
    expect(result.current).toMatchObject({ refreshing: true, items: [{ title: "Alien" }] });

    release();
    await waitFor(() =>
      expect(result.current).toMatchObject({ refreshing: false, items: [{ title: "Heat" }] }),
    );
    expect(v2Recorder.callsOf(PREVIEW)).toHaveLength(2);
  });
});

describe("useCollectionDraft create", () => {
  it("creates once: a Create after the collection exists sends nothing", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList());
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(
      () => useCollectionDraft(SERVER_SCOPE, { kind: "manual", libraryId: 1 }),
      { wrapper },
    );
    act(() => result.current.setDraft((draft) => ({ ...draft, name: "Weekend" })));

    let first: Awaited<ReturnType<typeof result.current.create>> = null;
    await act(async () => {
      first = await result.current.create();
    });
    expect(first).toMatchObject({ id: expect.any(String) });
    let second: Awaited<ReturnType<typeof result.current.create>> = null;
    await act(async () => {
      second = await result.current.create();
    });
    expect(second).toBeNull();
    expect(v2Recorder.callsOf("POST /api/v2/admin/collections")).toHaveLength(1);
  });
});

describe("useCollectionDraft editing", () => {
  async function edit() {
    const snapshot = await PERSONAL_SCOPE.fetchSnapshot("c1");
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    return renderHook(() => useCollectionDraft(PERSONAL_SCOPE, { snapshot, kind: "manual" }), {
      wrapper,
    }).result;
  }

  it("keeps a conflict through a later read until Keep mine or Use theirs", async () => {
    const result = await edit();
    act(() => result.current.setDraft((draft) => ({ ...draft, name: "Mine" })));
    v2Recorder.answer("GET /api/v2/collections/{id}", { ...getCollectionOk, name: "Theirs" });
    await act(() => result.current.syncWithServer());
    expect(result.current.conflicts).toEqual(["name"]);
    // A title write reads the collection again; nothing new changed there.
    await act(() => result.current.syncWithServer());
    expect(result.current.conflicts).toEqual(["name"]);
    expect(result.current.draft.name).toBe("Mine");
    act(() => result.current.resolveConflicts("mine"));
    await act(() => result.current.syncWithServer());
    expect(result.current.conflicts).toEqual([]);
  });

  it("keeps an image changed while a save ran staged", async () => {
    const result = await edit();
    let release!: () => void;
    const held = new Promise<void>((resolve) => (release = resolve));
    v2Recorder.answer("PATCH /api/v2/collections/{id}", () => held.then(() => getCollectionOk));
    const poster = (sourceUrl: string) => (draft: CollectionDraft) => ({
      ...draft,
      artwork: { ...draft.artwork, poster: { sourceUrl } },
    });
    act(() => result.current.setDraft(poster("https://images.example/first.png")));
    let saving!: Promise<boolean>;
    act(() => {
      saving = result.current.save();
    });
    act(() => result.current.setDraft(poster("https://images.example/second.png")));
    release();
    await act(async () => {
      expect(await saving).toBe(true);
    });
    expect(v2Recorder.callsOf("PUT /api/v2/collections/{id}/poster")[0]!.form).toEqual({
      source_url: "https://images.example/first.png",
    });
    expect(result.current.draft.artwork).toEqual({
      poster: { sourceUrl: "https://images.example/second.png" },
    });
    expect(result.current.isDirty).toBe(true);
    expect(result.current.artworkErrors).toEqual({});
  });
});

describe("useListedPoster", () => {
  const view = PERSONAL_SCOPE.toView(
    collectionFromV2(getCollectionOk as Parameters<typeof collectionFromV2>[0]),
  );

  function watch(awaitCollage = true) {
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    return renderHook(
      ({ version }: { version: string }) =>
        useListedPoster(PERSONAL_SCOPE, view, { awaitCollage, version }),
      { wrapper, initialProps: { version: "saved-1" } },
    );
  }

  const listCalls = () => v2Recorder.callsOf("GET /api/v2/collections").length;

  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    return () => vi.useRealTimers();
  });

  it("reads the list again while a collection with titles waits for its collage", async () => {
    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...getCollectionOk, item_count: 2, poster_url: "" }],
    });
    const { result } = watch();
    await waitFor(() => expect(listCalls()).toBe(1));
    expect(result.current).toEqual({ url: undefined, isCollage: false });

    v2Recorder.answer("GET /api/v2/collections", {
      items: [
        {
          ...getCollectionOk,
          item_count: 2,
          poster_url: "https://images.example/collage.webp",
          poster_is_collage: true,
        },
      ],
    });
    await act(() => vi.advanceTimersByTimeAsync(3_000));
    await waitFor(() =>
      expect(result.current).toEqual({
        url: "https://images.example/collage.webp",
        isCollage: true,
      }),
    );
    // The collage arrived: no more reads.
    const calls = listCalls();
    await act(() => vi.advanceTimersByTimeAsync(9_000));
    expect(listCalls()).toBe(calls);
  });

  it("gives up after a short while when no collage comes", async () => {
    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...getCollectionOk, item_count: 2, poster_url: "" }],
    });
    watch();
    await waitFor(() => expect(listCalls()).toBe(1));
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    const calls = listCalls();
    expect(calls).toBeGreaterThan(1);
    expect(calls).toBeLessThanOrEqual(7);
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    expect(listCalls()).toBe(calls);
  });

  it("waits again after a save, even once an earlier wait gave up", async () => {
    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...getCollectionOk, item_count: 2, poster_url: "" }],
    });
    const { rerender } = watch();
    await waitFor(() => expect(listCalls()).toBe(1));
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    const gaveUp = listCalls();
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(listCalls()).toBe(gaveUp);

    // The save moves the editor's version and reads the list again before
    // the server has built the new collage.
    rerender({ version: "saved-2" });
    await act(() => client.invalidateQueries({ queryKey: PERSONAL_SCOPE.keys.list }));
    const afterSave = listCalls();
    await act(() => vi.advanceTimersByTimeAsync(6_000));
    expect(listCalls()).toBeGreaterThan(afterSave);
  });

  it("doesn't wait for a collection without titles, or one that gets no collage", async () => {
    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...getCollectionOk, item_count: 0, poster_url: "" }],
    });
    watch();
    await waitFor(() => expect(listCalls()).toBe(1));
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(listCalls()).toBe(1);

    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...getCollectionOk, item_count: 2, poster_url: "" }],
    });
    watch(false);
    await waitFor(() => expect(listCalls()).toBe(2));
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(listCalls()).toBe(2);
  });
});
