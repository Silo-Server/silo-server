import { useEffect, useRef, useState } from "react";

import { apiBlob } from "@/api/client";
import type { FileVersion } from "@/api/types";
import { DocumentLoader, type BookDoc } from "@/reader/readest/libs/document";

type FoliateViewElement = HTMLElement & {
  open: (book: BookDoc) => Promise<void>;
  close?: () => void;
  init: (options: { lastLocation: string }) => Promise<void>;
  goToFraction: (fraction: number) => Promise<void>;
  next?: () => void;
  prev?: () => void;
  renderer?: HTMLElement & {
    setStyles?: (css: string) => void;
    render?: () => Promise<void>;
  };
};

export type ReaderLoadState = {
  objectUrl: string;
  filename: string;
};

export function ebookReadPath(contentID: string, fileID: number): string {
  return `/ebooks/${encodeURIComponent(contentID)}/files/${fileID}/read`;
}

export function readerFileFormat(file: FileVersion | undefined): string {
  if (!file) return "";
  const container = file.container?.trim().toLowerCase();
  if (container) return container.replace(/^\./, "");
  const fileName = file.file_name || file.file_path || "";
  if (fileName.toLowerCase().endsWith(".fb2.zip")) return "fbz";
  const match = /\.([a-z0-9]+)$/i.exec(fileName);
  return match?.[1]?.toLowerCase() ?? "";
}

export function readerMimeType(format: string): string {
  switch (format.toLowerCase()) {
    case "epub":
      return "application/epub+zip";
    case "pdf":
      return "application/pdf";
    case "mobi":
      return "application/x-mobipocket-ebook";
    case "azw":
      return "application/vnd.amazon.ebook";
    case "azw3":
      return "application/vnd.amazon.mobi8-ebook";
    case "cbz":
      return "application/vnd.comicbook+zip";
    case "cbr":
      return "application/vnd.comicbook-rar";
    case "fb2":
      return "application/x-fictionbook+xml";
    case "fbz":
      return "application/x-zip-compressed-fb2";
    case "md":
      return "text/markdown";
    case "txt":
      return "text/plain";
    default:
      return "application/octet-stream";
  }
}

function readerStyles() {
  return `
    :root {
      --theme-bg-color: #ffffff;
      --theme-fg-color: #171717;
      --override-color: true;
      color-scheme: light;
    }
    html, body {
      background: #ffffff !important;
      color: #171717 !important;
      font-family: Inter, ui-sans-serif, system-ui, sans-serif !important;
      font-size: 112% !important;
      hyphens: auto !important;
      line-height: 1.65 !important;
      max-width: 74ch !important;
    }
    body :where(p, span, div, li, blockquote, h1, h2, h3, h4, h5, h6,
                em, i, strong, b, code, pre, td, th, caption, figcaption,
                dt, dd, small, sub, sup, cite, q, mark) {
      color: #171717 !important;
    }
    p, li, blockquote { margin-block: 0.75em !important; }
    a { color: #2563eb !important; }
  `;
}

export default function FoliateBookReader({
  contentID,
  file,
  title,
  onFileLoaded,
}: {
  contentID: string;
  file: FileVersion;
  title: string;
  onFileLoaded?: (state: ReaderLoadState | null) => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<FoliateViewElement | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    let objectUrl: string | null = null;
    setLoading(true);
    setError("");
    onFileLoaded?.(null);

    async function open() {
      try {
        const format = readerFileFormat(file);
        const blob = await apiBlob(ebookReadPath(contentID, file.file_id));
        if (cancelled) return;

        objectUrl = URL.createObjectURL(blob);
        const filename = file.file_name || `${title}.${format || "ebook"}`;
        onFileLoaded?.({ objectUrl, filename });
        const documentFile = new File([blob], filename, {
          type: blob.type || readerMimeType(format),
        });
        const { book } = await new DocumentLoader(documentFile).open();
        if (cancelled) return;

        await import("foliate-js/view.js");
        const view = document.createElement("foliate-view") as FoliateViewElement;
        viewRef.current = view;
        containerRef.current?.replaceChildren(view);
        await view.open(book);

        const renderer = view.renderer;
        renderer?.setStyles?.(readerStyles());
        renderer?.setAttribute("gap", "24px");
        renderer?.setAttribute("margin", "24px");
        renderer?.setAttribute("max-inline-size", "74ch");
        renderer?.setAttribute("max-column-count", "2");
        await renderer?.render?.();
        await view.goToFraction(0);
        setLoading(false);
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Unable to open ebook");
          setLoading(false);
        }
      }
    }

    void open();
    return () => {
      cancelled = true;
      viewRef.current?.close?.();
      viewRef.current?.remove();
      viewRef.current = null;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
      onFileLoaded?.(null);
    };
  }, [contentID, file, onFileLoaded, title]);

  return (
    <div className="relative h-full w-full overflow-hidden bg-white text-neutral-950">
      <div ref={containerRef} className="h-full w-full" />
      {loading && !error && (
        <div className="absolute inset-0 flex items-center justify-center bg-white text-sm text-neutral-500">
          Loading reader...
        </div>
      )}
      {error && (
        <div className="absolute inset-0 flex items-center justify-center bg-white p-6 text-center text-sm text-red-600">
          {error}
        </div>
      )}
    </div>
  );
}
