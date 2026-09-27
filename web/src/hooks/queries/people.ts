import { useEffect } from "react";
import {
  type Query,
  type QueryClient,
  QueryObserver,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";

import { adminRefreshPerson, adminUpdatePerson } from "@/api/v2/people";
import type { ItemDetail, Person, UpdatePersonRequest } from "@/api/types";
import {
  getPerson,
  refreshPerson,
  getPeopleSearchCapabilities,
  searchPeople,
  type PersonRefreshResult,
  type PersonSearchMediaScope,
} from "@/api/v2/people";
import { scheduleWhenIdle } from "@/lib/routeChunkPrefetch";

import { personKeys } from "./keys";
import { isItemDetailQueryKey } from "./mediaSurfaceRefresh";

const PEOPLE_PREFETCH_CONCURRENCY = 3;
const PEOPLE_PREFETCH_STALE_TIME_MS = 5 * 60_000;

function fetchPeopleSearchCapabilities(queryClient: QueryClient) {
  return queryClient.fetchQuery({
    queryKey: personKeys.searchCapabilities(),
    queryFn: ({ signal }) => getPeopleSearchCapabilities({ signal }),
    staleTime: 5 * 60 * 1000,
  });
}

function isPersonItemDetail(query: Query, personId: string) {
  const item = query.state.data as ItemDetail | undefined;
  return (
    !!item &&
    isItemDetailQueryKey(query.queryKey, item.content_id) &&
    (item.cast?.some((credit) => credit.person_id === personId) ||
      item.crew?.some((credit) => credit.person_id === personId))
  );
}

export function invalidatePersonItemDetails(queryClient: QueryClient, personId: string) {
  return queryClient.invalidateQueries({
    predicate: (query) => isPersonItemDetail(query, personId),
  });
}

const observedPeople = new WeakMap<QueryClient, Set<string>>();

export function observePersonRefresh(queryClient: QueryClient, id: string) {
  const queryKey = personKeys.detail(id);
  if (!queryClient.getQueryCache().find({ queryKey, exact: true })) return;

  const refreshes = observedPeople.get(queryClient) ?? new Set<string>();
  observedPeople.set(queryClient, refreshes);
  if (refreshes.has(id)) return;

  const startedAt = Date.now();
  let photoUrl = queryClient.getQueryData<Person>(queryKey)?.photo_url;
  let photoRevision = 0;
  const readRevisions = new WeakMap<Query, number>();
  for (const itemQuery of queryClient.getQueryCache().getAll()) {
    if (itemQuery.state.fetchStatus === "fetching") readRevisions.set(itemQuery, photoRevision);
  }
  let refreshOnResume = false;
  const observer = new QueryObserver(queryClient, {
    queryKey,
    queryFn: ({ signal }) => getPerson(id, { signal }),
    staleTime: 0,
    retry: false,
    // Queue wait and photo caching can outlast the worker's per-person timeout.
    refetchInterval: () => (Date.now() - startedAt < 30_000 ? 3_000 : 30_000),
  });
  const query = observer.getCurrentQuery();
  const unsubscribeCache = queryClient.getQueryCache().subscribe((event) => {
    if (event.type === "removed" && event.query === query) {
      stop();
      return;
    }
    if (event.type === "updated" && event.query !== query) {
      if (event.action.type === "fetch") {
        readRevisions.set(event.query, photoRevision);
      } else if (event.action.type === "success" && !event.action.manual) {
        const revision = readRevisions.get(event.query);
        readRevisions.delete(event.query);
        // A dialog or prefetch can finish an old read after the photo invalidation.
        if (
          revision !== undefined &&
          revision < photoRevision &&
          isPersonItemDetail(event.query, id)
        ) {
          void queryClient.invalidateQueries({ predicate: (item) => item === event.query });
        }
      }
    }
    if (
      event.type === "removed" ||
      (event.query === query &&
        (event.type === "observerAdded" || event.type === "observerRemoved")) ||
      (event.type === "updated" && event.query !== query)
    ) {
      updateObservation();
    }
  });
  function stop() {
    unsubscribeCache();
    observer.destroy();
    refreshes.delete(id);
  }
  function updateObservation() {
    const observing = observer.hasListeners();
    const needed =
      query.getObserversCount() > (observing ? 1 : 0) ||
      queryClient
        .getQueryCache()
        .getAll()
        .some((item) => isPersonItemDetail(item, id));
    if (needed && !observing) {
      observer.subscribe((result) => {
        // Presigned URLs can rotate before the job finishes; this is not completion.
        if (
          result.isSuccess &&
          !result.isFetching &&
          (refreshOnResume || result.data.photo_url !== photoUrl)
        ) {
          refreshOnResume = false;
          if (result.data.photo_url !== photoUrl) photoRevision++;
          photoUrl = result.data.photo_url;
          void invalidatePersonItemDetails(queryClient, id);
        }
      });
    } else if (!needed && observing) {
      // Pause during navigation. Resume when an uncached related detail arrives;
      // the cache listener is disposed when this inactive person query expires.
      // That detail may have fetched its old photo before the person was updated.
      refreshOnResume = true;
      observer.destroy();
    }
  }
  refreshes.add(id);
  updateObservation();
}

/**
 * Warms person detail for the people a cast row shows, so opening one renders
 * from cache. Reads start once the page is idle, run a few at a time, skip
 * people already cached, and are marked as prefetches so they do not queue
 * provider refreshes. A server that does not advertise `person_prefetch`
 * would reject the marker, so nothing is prefetched from it. Unmounting drops
 * the remaining queue and cancels reads nobody else is waiting on.
 */
export function usePrefetchPeople(personIds: readonly string[], enabled = true) {
  const queryClient = useQueryClient();
  const idsKey = [...new Set(personIds)].join(",");

  useEffect(() => {
    if (!enabled || !idsKey) return;
    const pending = idsKey.split(",");
    const inFlight = new Set<string>();
    let stopped = false;

    const drain = async () => {
      for (let id = pending.shift(); id !== undefined && !stopped; id = pending.shift()) {
        const personId = id;
        inFlight.add(personId);
        const queryKey = personKeys.detail(personId);
        let openedDuringRead = false;
        await queryClient.prefetchQuery({
          queryKey,
          queryFn: async ({ signal }) => {
            const person = await getPerson(personId, { signal, prefetch: true });
            const query = queryClient.getQueryCache().find({ queryKey, exact: true });
            openedDuringRead = !!query?.getObserversCount();
            return person;
          },
          staleTime: PEOPLE_PREFETCH_STALE_TIME_MS,
          retry: false,
        });
        inFlight.delete(personId);
        // A person page that opened during the read shared the prefetch. It
        // renders that result while its own query reads again as a view the
        // server can queue a due refresh for; a failed view read keeps the data.
        if (openedDuringRead) {
          void queryClient.refetchQueries({ queryKey, exact: true, type: "active" });
        }
      }
    };
    const start = async () => {
      const capabilities = await fetchPeopleSearchCapabilities(queryClient).catch(() => null);
      if (stopped || !capabilities?.person_prefetch) return;
      for (let i = 0; i < PEOPLE_PREFETCH_CONCURRENCY; i++) void drain();
    };
    const cancelIdle = scheduleWhenIdle(() => void start());

    return () => {
      stopped = true;
      cancelIdle();
      for (const personId of inFlight) {
        void queryClient.cancelQueries({
          queryKey: personKeys.detail(personId),
          exact: true,
          predicate: (query) => query.getObserversCount() === 0,
        });
      }
    };
  }, [enabled, idsKey, queryClient]);
}

export function usePersonSearch(
  query: string,
  limit = 20,
  enabled = true,
  mediaScope?: PersonSearchMediaScope,
) {
  const normalizedQuery = query.trim();
  const queryClient = useQueryClient();

  return useQuery({
    queryKey: personKeys.search(normalizedQuery, limit, mediaScope),
    queryFn: async ({ signal }) => {
      const capabilities = await fetchPeopleSearchCapabilities(queryClient);
      // This capability also guarantees viewer access filtering for All.
      if (!capabilities.people_media_scope) return [];
      return searchPeople(normalizedQuery, limit, { signal, mediaScope });
    },
    enabled: enabled && normalizedQuery.length > 0,
    staleTime: 5 * 60 * 1000,
  });
}

type RefreshPersonResult =
  | {
      mode: "admin";
      id: string;
      person: Person;
    }
  | {
      mode: "queued";
      response: PersonRefreshResult;
    };

export function useRefreshPerson(id: string | undefined, isAdmin: boolean) {
  const queryClient = useQueryClient();

  return useMutation({
    retry: false,
    onMutate: () =>
      queryClient.getQueryCache().find({ queryKey: personKeys.detail(id!), exact: true }),
    mutationFn: async (): Promise<RefreshPersonResult> => {
      if (!id) {
        throw new Error("Person ID is required");
      }

      if (isAdmin) {
        return {
          mode: "admin",
          id,
          person: await adminRefreshPerson(id),
        };
      }

      return {
        mode: "queued",
        response: await refreshPerson(id),
      };
    },
    onSuccess: async (result, _variables, personQuery) => {
      const refreshedId = result.mode === "admin" ? result.id : result.response.person_id;
      const currentPersonQuery = () =>
        queryClient.getQueryCache().find({
          queryKey: personKeys.detail(refreshedId),
          exact: true,
        });
      if (personQuery && currentPersonQuery() !== personQuery) return;

      if (result.mode === "admin") {
        queryClient.setQueryData(personKeys.detail(refreshedId), result.person);
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: personKeys.detail(refreshedId) }),
          invalidatePersonItemDetails(queryClient, refreshedId),
        ]);
      }

      // Admin metadata refreshes can still leave an asynchronous photo-cache job.
      if (personQuery && currentPersonQuery() === personQuery) {
        observePersonRefresh(queryClient, refreshedId);
      }
      toast.success(
        result.mode === "admin" ? "Person metadata refreshed" : "Person refresh queued",
      );
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Refresh failed");
    },
  });
}

export function useUpdatePersonMetadata(id: string | undefined) {
  const queryClient = useQueryClient();

  return useMutation({
    retry: false,
    mutationFn: (data: UpdatePersonRequest) => {
      if (!id) {
        throw new Error("Person ID is required");
      }

      return adminUpdatePerson(id, data);
    },
    onSuccess: async (updatedPerson) => {
      if (!id) {
        return;
      }

      queryClient.setQueryData(personKeys.detail(id), updatedPerson);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: personKeys.detail(id) }),
        invalidatePersonItemDetails(queryClient, id),
      ]);
      toast.success("Person metadata saved");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save metadata");
    },
  });
}
