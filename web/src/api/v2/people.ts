/**
 * Viewer-facing people reads and the coalescing refresh action on the v2
 * contract. Person IDs stay strings so links preserve their full precision.
 */
import type { Person, UpdatePersonRequest } from "@/api/types";
import { personFromV2 } from "@/api/v2/catalog";
import { v2, type V2Result, type V2Query } from "@/api/v2/request";

export type PersonRefreshResult = V2Result<"POST /api/v2/catalog/people/{id}/refresh">;
export type PersonSearchMediaScope = NonNullable<
  V2Query<"GET /api/v2/catalog/people">["media_scope"]
>;

export function getPeopleSearchCapabilities(options?: Pick<RequestInit, "signal">) {
  return v2("GET /api/v2/catalog/search/capabilities", { signal: options?.signal ?? undefined });
}

export async function searchPeople(
  query: string,
  limit = 20,
  options?: Pick<RequestInit, "signal"> & { mediaScope?: PersonSearchMediaScope },
): Promise<Person[]> {
  const people = await v2("GET /api/v2/catalog/people", {
    query: { q: query, limit, media_scope: options?.mediaScope },
    signal: options?.signal ?? undefined,
  });
  return people.items.map(personFromV2);
}

/**
 * Reads one person. A plain read counts as a view and can queue a provider
 * refresh; `prefetch` marks a speculative cache warm-up that must not.
 */
export async function getPerson(
  id: string,
  options?: Pick<RequestInit, "signal"> & { prefetch?: boolean },
): Promise<Person> {
  return personFromV2(
    await v2("GET /api/v2/catalog/people/{id}", {
      path: { id },
      query: options?.prefetch ? { prefetch: true } : undefined,
      signal: options?.signal ?? undefined,
    }),
  );
}

/** Queues a metadata refresh for a person; the server coalesces repeats. */
export async function refreshPerson(id: string): Promise<PersonRefreshResult> {
  return v2("POST /api/v2/catalog/people/{id}/refresh", { path: { id } });
}

/** Administrator refresh waits for the provider instead of queuing viewer work. */
export async function adminRefreshPerson(id: string): Promise<Person> {
  return personFromV2(
    await v2("POST /api/v2/admin/people/{id}/refresh", {
      path: { id },
      retryAuthentication: false,
    }),
  );
}
export async function adminUpdatePerson(id: string, data: UpdatePersonRequest): Promise<Person> {
  return personFromV2(
    await v2("PATCH /api/v2/admin/people/{id}", {
      path: { id },
      body: {
        ...data,
        birth_date: data.birth_date ?? undefined,
        death_date: data.death_date ?? undefined,
      },
      retryAuthentication: false,
    }),
  );
}
