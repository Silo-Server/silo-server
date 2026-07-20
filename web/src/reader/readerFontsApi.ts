import { api, apiBlob } from "@/api/client";

export type ReaderFontMeta = {
  id: number;
  name: string;
  filename: string;
  created_at: string;
};

const READER_FONTS_PATH = "/ebooks/reader-fonts";

function readerFontPath(id: number): string {
  return `${READER_FONTS_PATH}/${id}`;
}

export async function fetchReaderFonts(): Promise<ReaderFontMeta[]> {
  const envelope = await api<{ fonts?: ReaderFontMeta[] }>(READER_FONTS_PATH);
  return Array.isArray(envelope.fonts) ? envelope.fonts : [];
}

export async function uploadReaderFont(file: File): Promise<ReaderFontMeta> {
  const formData = new FormData();
  formData.append("font", file);
  return api<ReaderFontMeta>(READER_FONTS_PATH, {
    method: "POST",
    body: formData,
  });
}

export async function deleteReaderFont(id: number): Promise<void> {
  await api<void>(readerFontPath(id), { method: "DELETE" });
}

export function readerFontFileUrl(id: number): string {
  return `/api/v1${readerFontPath(id)}/file`;
}

/**
 * Fetches an uploaded reader font's bytes through the authenticated API
 * client (bearer token + X-Profile-Id header) and hands back a `blob:` URL
 * for it. The reader's CSS cannot point `@font-face { src: url(...) }`
 * directly at the `readerFontFileUrl` path: that's a browser-native fetch
 * triggered by the rendering engine, which never carries our in-memory
 * bearer token or profile header, so the request 401s silently and the font
 * never renders. A `blob:` URL created here (in the parent document) sidesteps
 * that: it can be dereferenced from the same-origin `srcdoc` iframes foliate
 * renders book content into, so it works as an `@font-face` src without a
 * second authenticated request from inside the iframe.
 *
 * Callers own the returned URL and must `URL.revokeObjectURL` it once no
 * longer needed to avoid leaking the underlying blob.
 */
export async function fetchReaderFontObjectUrl(id: number): Promise<string> {
  const blob = await apiBlob(`${readerFontPath(id)}/file`);
  return URL.createObjectURL(blob);
}
