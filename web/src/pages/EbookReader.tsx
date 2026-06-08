import { useCallback, useMemo, useRef, useState } from "react";
import { ArrowLeft, ChevronLeft, ChevronRight, Download, Library, Loader2 } from "lucide-react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";

import type { FileVersion } from "@/api/types";
import PageBack from "@/components/PageBack";
import { Button } from "@/components/ui/button";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import FoliateBookReader, {
  formatReaderProgress,
  isReaderSupportedFile,
  readerFileFormat,
  type FoliateBookReaderHandle,
  type ReaderLoadState,
} from "@/reader/FoliateBookReader";

function chooseReaderFile(files: FileVersion[], requestedID: number | null): FileVersion | undefined {
  const requested = requestedID ? files.find((file) => file.file_id === requestedID) : undefined;
  if (requested && isReaderSupportedFile(requested)) return requested;
  return (
    files.find((file) => readerFileFormat(file) === "epub") ??
    files.find((file) => isReaderSupportedFile(file)) ??
    files[0]
  );
}

function readerFileLabel(file: FileVersion): string {
  const format = readerFileFormat(file).toUpperCase();
  const name = file.file_name || file.file_path?.split(/[\\/]/).pop() || `File ${file.file_id}`;
  return format ? `${format} · ${name}` : name;
}

export default function EbookReader() {
  const { contentId = "" } = useParams<{ contentId: string }>();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const requestedFileID = Number(searchParams.get("file_id") || "");
  const libraryIdParam = searchParams.get("libraryId");
  const { data: item, isLoading, error } = useCatalogItemDetail(contentId || undefined);
  const selectedFile = useMemo(
    () => chooseReaderFile(item?.versions ?? [], Number.isFinite(requestedFileID) ? requestedFileID : null),
    [item?.versions, requestedFileID],
  );
  const readerFiles = useMemo(
    () => (item?.versions ?? []).filter((file) => isReaderSupportedFile(file)),
    [item?.versions],
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
  const handleFileChange = useCallback(
    (fileID: string) => {
      if (!contentId) return;
      const nextParams = new URLSearchParams();
      nextParams.set("file_id", fileID);
      if (libraryIdParam) {
        nextParams.set("libraryId", libraryIdParam);
      }
      navigate(`/reader/ebook/${encodeURIComponent(contentId)}?${nextParams.toString()}`, {
        replace: true,
      });
    },
    [contentId, libraryIdParam, navigate],
  );
  const backHref = `/item/${encodeURIComponent(item.content_id)}${
    libraryIdParam ? `?libraryId=${encodeURIComponent(libraryIdParam)}` : ""
  }`;

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
            <Link to={backHref}>
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
          {isReaderSupportedFile(selectedFile) && readerFiles.length > 1 && (
            <select
              aria-label="Reader file"
              value={selectedFile.file_id}
              onChange={(event) => handleFileChange(event.target.value)}
              className="border-border bg-background text-foreground hidden h-8 max-w-44 rounded-md border px-2 text-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 sm:block"
            >
              {readerFiles.map((file) => (
                <option key={file.file_id} value={file.file_id}>
                  {readerFileLabel(file)}
                </option>
              ))}
            </select>
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
        {isReaderSupportedFile(selectedFile) ? (
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
