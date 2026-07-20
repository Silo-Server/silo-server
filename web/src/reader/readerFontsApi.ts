import { api } from "@/api/client";

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
