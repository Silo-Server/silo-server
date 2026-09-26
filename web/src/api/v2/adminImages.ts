import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "./request";
import type { ApplyItemImageRequest, ItemImagesResponse } from "@/api/types";
export async function getAdminItemImages(
  id: string,
  signal?: AbortSignal,
): Promise<ItemImagesResponse> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  const images: ItemImagesResponse["images"] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  let current: ItemImagesResponse["current"] = {};
  const provider_errors: Record<string, string> = {};
  for (;;) {
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    const result = await v2("GET /api/v2/admin/items/{id}/images", {
      path: { id },
      query: { limit: 200, cursor },
      profileContext,
      signal,
    });
    images.push(...result.items);
    current = result.current;
    Object.assign(provider_errors, result.provider_errors);
    if (!result.page.has_more) break;
    const next = result.page.next_cursor;
    if (!next || seen.has(next)) throw new Error("Image choices changed. Reload to try again.");
    seen.add(next);
    cursor = next;
  }
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return { images, current, provider_errors };
}
export function applyAdminItemImage(id: string, request: ApplyItemImageRequest) {
  return v2("POST /api/v2/admin/items/{id}/images/apply", {
    path: { id },
    body: request,
    retryAuthentication: false,
  });
}
