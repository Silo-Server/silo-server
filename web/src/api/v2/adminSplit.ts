import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "./request";
import type { ItemFile, ItemSplitRequest } from "@/api/types";
export async function getAdminItemFiles(id: string, signal?: AbortSignal) {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  const files: ItemFile[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  for (;;) {
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    const page = await v2("GET /api/v2/admin/items/{id}/files", {
      path: { id },
      query: { limit: 200, cursor },
      signal,
      profileContext,
    });
    files.push(...page.items);
    if (!page.page?.has_more) break;
    const next = page.page.next_cursor;
    if (!next || seen.has(next)) throw new Error("Incomplete item file list. Reload to try again.");
    seen.add(next);
    cursor = next;
  }
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return { files };
}
export function splitAdminItem(id: string, request: ItemSplitRequest) {
  return v2("POST /api/v2/admin/items/{id}/split", {
    path: { id },
    body: request,
    retryAuthentication: false,
  });
}
