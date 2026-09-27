import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2, type V2Body } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";

type WireAnnotation = components["schemas"]["EbookAnnotation"];
export type EbookReaderAnnotation = Omit<WireAnnotation, "kind" | "metadata"> & {
  kind: "highlight" | "note" | "bookmark";
  metadata?: Record<string, unknown>;
};
export type EbookReaderAnnotationInput = Partial<
  Pick<
    EbookReaderAnnotation,
    "kind" | "cfi_range" | "location" | "selected_text" | "note" | "style" | "color" | "metadata"
  >
>;
type CreateBody = V2Body<"POST /api/v2/ebooks/{content_id}/annotations">;

export type EbookAnnotationSession = {
  profileContext: ProfileRequestContextSnapshot | null;
  // An uncertain create retains its immutable request until that same intent succeeds.
  creates: Map<string, CreateBody>;
};
export function createEbookAnnotationSession(): EbookAnnotationSession {
  return { profileContext: captureProfileRequestContext(), creates: new Map() };
}
function authority(session: EbookAnnotationSession): ProfileRequestContextSnapshot {
  if (!session.profileContext || !isCapturedProfileAuthorityActive(session.profileContext))
    throw new StaleApiRequestContextError();
  return session.profileContext;
}
function annotationFromV2(row: WireAnnotation): EbookReaderAnnotation {
  if (!row.etag || !["highlight", "note", "bookmark"].includes(row.kind))
    throw new Error("The annotation response has no valid kind or validator.");
  return { ...row, kind: row.kind as EbookReaderAnnotation["kind"] };
}
export async function fetchEbookReaderAnnotations(
  contentID: string,
  session: EbookAnnotationSession,
): Promise<EbookReaderAnnotation[]> {
  const profileContext = authority(session);
  const items = new Map<string, EbookReaderAnnotation>();
  const seen = new Set<string>();
  let cursor: string | undefined;
  for (let page = 0; page < 100; page++) {
    const result = await v2("GET /api/v2/ebooks/{content_id}/annotations", {
      path: { content_id: contentID },
      query: { limit: 50, cursor },
      profileContext,
    });
    authority(session);
    for (const row of result.items) items.set(row.id, annotationFromV2(row));
    if (!result.page?.has_more) return [...items.values()];
    const next = result.page.next_cursor;
    if (!next || seen.has(next)) throw new Error("The annotation cursor did not advance.");
    seen.add(next);
    cursor = next;
  }
  throw new Error("This book has too many annotations to load at once.");
}
export async function createEbookReaderAnnotation(
  contentID: string,
  annotation: EbookReaderAnnotationInput,
  session: EbookAnnotationSession,
): Promise<EbookReaderAnnotation> {
  const profileContext = authority(session);
  const key = JSON.stringify({ contentID, annotation });
  let body = session.creates.get(key);
  if (!body) {
    body = { ...JSON.parse(JSON.stringify(annotation)), id: crypto.randomUUID() } as CreateBody;
    session.creates.set(key, body);
  }
  const result = await v2("POST /api/v2/ebooks/{content_id}/annotations", {
    path: { content_id: contentID },
    body,
    profileContext,
    retryAuthentication: false,
  });
  authority(session);
  const row = annotationFromV2(result);
  session.creates.delete(key);
  return row;
}
export async function updateEbookReaderAnnotation(
  contentID: string,
  current: EbookReaderAnnotation,
  annotation: EbookReaderAnnotationInput,
  session: EbookAnnotationSession,
): Promise<EbookReaderAnnotation> {
  const profileContext = authority(session);
  if (!current.etag) throw new Error("Reload annotations before editing.");
  const result = await v2("PATCH /api/v2/ebooks/{content_id}/annotations/{annotation_id}", {
    path: { content_id: contentID, annotation_id: current.id },
    body: annotation,
    headers: { "If-Match": current.etag },
    profileContext,
    retryAuthentication: false,
  });
  authority(session);
  return annotationFromV2(result);
}
export async function deleteEbookReaderAnnotation(
  contentID: string,
  current: EbookReaderAnnotation,
  session: EbookAnnotationSession,
): Promise<void> {
  const profileContext = authority(session);
  if (!current.etag) throw new Error("Reload annotations before deleting.");
  await v2("DELETE /api/v2/ebooks/{content_id}/annotations/{annotation_id}", {
    path: { content_id: contentID, annotation_id: current.id },
    headers: { "If-Match": current.etag },
    profileContext,
    retryAuthentication: false,
  });
  authority(session);
}
