import { api, apiKeepalive } from "@/api/client";

export type EbookReaderConfigEnvelope = {
  content_id?: string;
  config?: Record<string, unknown>;
  updated_at?: string;
};

export type EbookReaderAnnotation = {
  id: string;
  content_id: string;
  kind: "highlight" | "note" | "bookmark";
  cfi_range?: string;
  location?: string;
  selected_text: string;
  note: string;
  style: string;
  color: string;
  metadata?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
};

export type EbookReaderAnnotationInput = Partial<
  Pick<
    EbookReaderAnnotation,
    "kind" | "cfi_range" | "location" | "selected_text" | "note" | "style" | "color" | "metadata"
  >
>;

export function ebookReaderConfigPath(contentID: string): string {
  return `/ebooks/${encodeURIComponent(contentID)}/reader-config`;
}

export function ebookReaderAnnotationsPath(contentID: string): string {
  return `/ebooks/${encodeURIComponent(contentID)}/annotations`;
}

export function ebookReaderAnnotationPath(contentID: string, annotationID: string): string {
  return `${ebookReaderAnnotationsPath(contentID)}/${encodeURIComponent(annotationID)}`;
}

export async function fetchEbookReaderConfig(contentID: string): Promise<Record<string, unknown>> {
  const envelope = await api<EbookReaderConfigEnvelope>(ebookReaderConfigPath(contentID));
  return envelope.config && typeof envelope.config === "object" && !Array.isArray(envelope.config)
    ? envelope.config
    : {};
}

export async function saveEbookReaderConfig(
  contentID: string,
  config: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const envelope = await api<EbookReaderConfigEnvelope>(ebookReaderConfigPath(contentID), {
    method: "PUT",
    body: JSON.stringify({ config }),
  });
  return envelope.config && typeof envelope.config === "object" && !Array.isArray(envelope.config)
    ? envelope.config
    : {};
}

/**
 * Fire-and-forget config save for page unload (pagehide), when a normal
 * authenticated request can no longer complete or refresh tokens.
 */
export function saveEbookReaderConfigKeepalive(
  contentID: string,
  config: Record<string, unknown>,
): void {
  apiKeepalive(ebookReaderConfigPath(contentID), {
    method: "PUT",
    body: JSON.stringify({ config }),
  });
}

export async function fetchEbookReaderAnnotations(
  contentID: string,
): Promise<EbookReaderAnnotation[]> {
  const envelope = await api<{ items?: EbookReaderAnnotation[] }>(
    ebookReaderAnnotationsPath(contentID),
  );
  return Array.isArray(envelope.items) ? envelope.items : [];
}

export async function createEbookReaderAnnotation(
  contentID: string,
  annotation: EbookReaderAnnotationInput,
): Promise<EbookReaderAnnotation> {
  return api<EbookReaderAnnotation>(ebookReaderAnnotationsPath(contentID), {
    method: "POST",
    body: JSON.stringify(annotation),
  });
}

export async function updateEbookReaderAnnotation(
  contentID: string,
  annotationID: string,
  annotation: EbookReaderAnnotationInput,
): Promise<EbookReaderAnnotation> {
  return api<EbookReaderAnnotation>(ebookReaderAnnotationPath(contentID, annotationID), {
    method: "PATCH",
    body: JSON.stringify(annotation),
  });
}

export async function deleteEbookReaderAnnotation(
  contentID: string,
  annotationID: string,
): Promise<void> {
  await api<void>(ebookReaderAnnotationPath(contentID, annotationID), {
    method: "DELETE",
  });
}
