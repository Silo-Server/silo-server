import { api } from "@/api/client";

export type EbookReaderConfigEnvelope = {
  content_id?: string;
  config?: Record<string, unknown>;
  updated_at?: string;
};

export function ebookReaderConfigPath(contentID: string): string {
  return `/ebooks/${encodeURIComponent(contentID)}/reader-config`;
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
