import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, Download, Library, Loader2 } from "lucide-react";
import { Link, useParams, useSearchParams } from "react-router";

import { apiBlob } from "@/api/client";
import type { FileVersion } from "@/api/types";
import PageBack from "@/components/PageBack";
import { Button } from "@/components/ui/button";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";

const READEST_FORMATS = new Set([
  "epub",
  "pdf",
  "mobi",
  "azw",
  "azw3",
  "cbz",
  "cbr",
  "fb2",
  "fbz",
  "txt",
  "md",
]);

function fileFormat(file: FileVersion | undefined): string {
  if (!file) return "";
  const container = file.container?.trim().toLowerCase();
  if (container) return container.replace(/^\./, "");
  const fileName = file.file_name || file.file_path || "";
  const match = /\.([a-z0-9]+)$/i.exec(fileName);
  return match?.[1]?.toLowerCase() ?? "";
}

function chooseReaderFile(files: FileVersion[], requestedID: number | null): FileVersion | undefined {
  const requested = requestedID ? files.find((file) => file.file_id === requestedID) : undefined;
  if (requested) return requested;
  return (
    files.find((file) => fileFormat(file) === "epub") ??
    files.find((file) => READEST_FORMATS.has(fileFormat(file))) ??
    files[0]
  );
}

function readPath(contentID: string, fileID: number): string {
  return `/ebooks/${encodeURIComponent(contentID)}/files/${fileID}/read`;
}

export default function EbookReader() {
  const { contentId = "" } = useParams<{ contentId: string }>();
  const [searchParams] = useSearchParams();
  const requestedFileID = Number(searchParams.get("file_id") || "");
  const { data: item, isLoading, error } = useCatalogItemDetail(contentId || undefined);
  const selectedFile = useMemo(
    () => chooseReaderFile(item?.versions ?? [], Number.isFinite(requestedFileID) ? requestedFileID : null),
    [item?.versions, requestedFileID],
  );
  const format = fileFormat(selectedFile);
  const [objectUrl, setObjectUrl] = useState<string | null>(null);
  const [fileError, setFileError] = useState<string | null>(null);

  useEffect(() => {
    if (!contentId || !selectedFile) return;
    let cancelled = false;
    let nextUrl: string | null = null;
    setFileError(null);
    setObjectUrl(null);

    void apiBlob(readPath(contentId, selectedFile.file_id))
      .then((blob) => {
        if (cancelled) return;
        nextUrl = URL.createObjectURL(blob);
        setObjectUrl(nextUrl);
      })
      .catch(() => {
        if (!cancelled) setFileError("Unable to open this ebook file.");
      });

    return () => {
      cancelled = true;
      if (nextUrl) URL.revokeObjectURL(nextUrl);
    };
  }, [contentId, selectedFile]);

  if (isLoading) {
    return (
      <div className="flex min-h-[70vh] items-center justify-center">
        <Loader2 className="size-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (error || !item || item.type !== "ebook") {
    return (
      <div className="page-shell py-10">
        <PageBack />
        <div className="mt-10 text-sm text-muted-foreground">Ebook not found.</div>
      </div>
    );
  }

  if (!selectedFile) {
    return (
      <div className="page-shell py-10">
        <PageBack />
        <div className="mt-10 text-sm text-muted-foreground">No ebook files found.</div>
      </div>
    );
  }

  const canEmbed = objectUrl && (format === "pdf" || format === "txt" || format === "md");

  return (
    <div className="min-h-screen bg-background">
      <header className="sticky top-0 z-20 border-b border-border/70 bg-background/95 backdrop-blur">
        <div className="flex h-14 items-center gap-3 px-4">
          <Button asChild variant="ghost" size="icon" aria-label="Back to ebook">
            <Link to={`/item/${encodeURIComponent(item.content_id)}`}>
              <ArrowLeft className="size-5" />
            </Link>
          </Button>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">{item.title}</div>
            <div className="truncate text-xs text-muted-foreground">{format.toUpperCase()}</div>
          </div>
          {objectUrl && (
            <Button asChild variant="outline" size="sm">
              <a href={objectUrl} download={selectedFile.file_name || `${item.title}.${format || "ebook"}`}>
                <Download className="size-4" />
                File
              </a>
            </Button>
          )}
        </div>
      </header>

      <main className="h-[calc(100vh-3.5rem)]">
        {canEmbed ? (
          <iframe title={item.title} src={objectUrl} className="h-full w-full border-0 bg-white" />
        ) : (
          <div className="flex h-full items-center justify-center px-6">
            <div className="max-w-md text-center">
              <Library className="mx-auto mb-4 size-10 text-muted-foreground" />
              <h1 className="text-lg font-semibold">{item.title}</h1>
              <p className="mt-2 text-sm text-muted-foreground">
                {fileError ?? `${format.toUpperCase()} is ready for the Readest reader port.`}
              </p>
              {objectUrl && (
                <Button asChild className="mt-5">
                  <a href={objectUrl} download={selectedFile.file_name || `${item.title}.${format || "ebook"}`}>
                    <Download className="size-4" />
                    Open File
                  </a>
                </Button>
              )}
            </div>
          </div>
        )}
      </main>
    </div>
  );
}
