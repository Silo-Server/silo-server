import {
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";

type Page<T> = { items: T[]; page?: { has_more: boolean; next_cursor?: string } };

export async function readAutoscanPages<T, R>(
  profileContext: ProfileRequestContextSnapshot,
  load: (cursor?: string) => Promise<Page<T>>,
  convert: (items: T[]) => R[],
  pageError: string,
  continuationError: string,
  limitError: string,
): Promise<R[]> {
  const rows: R[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  for (let page = 0; page < 100; page++) {
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    const result = await load(cursor);
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    if (!result.page || typeof result.page.has_more !== "boolean") throw new Error(pageError);
    rows.push(...convert(result.items));
    if (!result.page.has_more) return rows;
    const next = result.page.next_cursor;
    if (!next || seen.has(next)) throw new Error(continuationError);
    seen.add(next);
    cursor = next;
  }
  throw new Error(limitError);
}
