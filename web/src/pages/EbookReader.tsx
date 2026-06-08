import { useCallback, useMemo, useRef, useState } from "react";
import { ArrowLeft, ChevronLeft, ChevronRight, Download, Library, Loader2 } from "lucide-react";
import { Link, useParams, useSearchParams } from "react-router";

import type { FileVersion } from "@/api/types";
import PageBack from "@/components/PageBack";
import { Button } from "@/components/ui/button";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import FoliateBookReader, {
  formatReaderProgress,
  readerFileFormat,
  type FoliateBookReaderHandle,
  type ReaderLoadState,
} from "@/reader/FoliateBookReader";

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

function chooseReaderFile(files: FileVersion[], requestedID: number | null): FileVersion | undefined {
  const requested = requestedID ? files.find((file) => file.file_id === requestedID) : undefined;
  if (requested) return requested;
  return (
    files.find((file) => readerFileFormat(file) === "epub") ??
    files.find((file) => READEST_FORMATS.has(readerFileFormat(file))) ??
    files[0]
  );
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
  const format = readerFileFormat(selectedFile);
  const readerRef = useRef<FoliateBookReaderHandle>(null);
  const [loadedFile, setLoadedFile] = useState<ReaderLoadState | null>(null);
  const [readerProgress, setReaderProgress] = useState<number | null>(null);
  const progressLabel = formatReaderProgress(readerProgress);
  const handleFileLoaded = useCallback((state: ReaderLoadState | null) => {
    setLoadedFile(state);
  }, []);
  const handleProgressChange = useCallback((progress: number | null) => {
    setReaderProgress(progress);
  }, []);

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
          {progressLabel && (
            <div className="hidden min-w-12 text-center text-xs tabular-nums text-muted-foreground sm:block">
              {progressLabel}
            </div>
          )}
          <div className="flex shrink-0 items-center gap-1">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Previous page"
              title="Previous page"
              onClick={() => readerRef.current?.prev()}
            >
              <ChevronLeft className="size-5" />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Next page"
              title="Next page"
              onClick={() => readerRef.current?.next()}
            >
              <ChevronRight className="size-5" />
            </Button>
          </div>
          {loadedFile && (
            <Button asChild variant="outline" size="sm">
              <a href={loadedFile.objectUrl} download={loadedFile.filename}>
                <Download className="size-4" />
                File
              </a>
            </Button>
          )}
        </div>
      </header>

      <main className="h-[calc(100vh-3.5rem)]">
        {READEST_FORMATS.has(format) ? (
          <FoliateBookReader
            ref={readerRef}
            contentID={contentId}
            file={selectedFile}
            title={item.title}
            onFileLoaded={handleFileLoaded}
            onProgressChange={handleProgressChange}
          />
        ) : (
          <div className="flex h-full items-center justify-center px-6">
            <div className="max-w-md text-center">
              <Library className="mx-auto mb-4 size-10 text-muted-foreground" />
              <h1 className="text-lg font-semibold">{item.title}</h1>
              <p className="mt-2 text-sm text-muted-foreground">Unsupported ebook format.</p>
            </div>
          </div>
        )}
      </main>
    </div>
  );
}
