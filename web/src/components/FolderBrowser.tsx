import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowUp, Check, FolderOpen, RefreshCw } from "lucide-react";

import { fetchFilesystemBrowse, useFilesystemBrowse } from "@/hooks/queries/admin/libraries";
import { adminKeys } from "@/hooks/queries/keys";
import { useDebounce } from "@/hooks/useDebounce";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import PathAutocompleteInput from "@/components/PathAutocompleteInput";
import { useT, type MessageKey } from "@/i18n";

interface FolderBrowserProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSelect: (paths: string[]) => void;
  existingPaths?: string[];
}

const ROOT_PATH = "/";
// An example absolute path, which is data in every language.
const PATH_PLACEHOLDER = "/mnt/media";

export default function FolderBrowser({
  open,
  onOpenChange,
  onSelect,
  existingPaths = [],
}: FolderBrowserProps) {
  const t = useT();
  const [currentPath, setCurrentPath] = useState(ROOT_PATH);
  const [draftPath, setDraftPath] = useState(ROOT_PATH);
  // A validation failure: the server's text, or a catalog message of the dialog's own.
  const [validationError, setValidationError] = useState<string | { key: MessageKey }>("");
  const [selectedPaths, setSelectedPaths] = useState<Set<string>>(new Set());
  const queryClient = useQueryClient();
  const debouncedDraftPath = useDebounce(draftPath.trim(), 200);
  const browseQuery = useFilesystemBrowse(currentPath);
  const { data, isLoading, isFetching, error, refetch } = browseQuery;

  const resolvedPath = data?.path ?? currentPath;
  const alreadyAdded = existingPaths.includes(resolvedPath);
  const parentPath = data?.parent ?? resolvedPath;
  const canGoUp = parentPath !== resolvedPath;

  useEffect(() => {
    if (
      debouncedDraftPath.length === 0 ||
      !debouncedDraftPath.startsWith(ROOT_PATH) ||
      debouncedDraftPath === currentPath
    ) {
      return;
    }

    let cancelled = false;

    queryClient
      .fetchQuery({
        queryKey: adminKeys.filesystemBrowse(debouncedDraftPath),
        queryFn: () => fetchFilesystemBrowse(debouncedDraftPath),
        staleTime: 60_000,
      })
      .then((nextFolder) => {
        if (cancelled) {
          return;
        }

        setValidationError("");
        setCurrentPath((prev) => (prev === nextFolder.path ? prev : nextFolder.path));
      })
      .catch(() => {
        // Ignore transient live-browse failures while the user is still typing.
      });

    return () => {
      cancelled = true;
    };
  }, [currentPath, debouncedDraftPath, queryClient]);

  function navigate(nextPath: string) {
    setValidationError("");
    setCurrentPath(nextPath);
    setDraftPath(nextPath);
  }

  async function handleBrowseSubmit() {
    const trimmed = draftPath.trim();
    if (!trimmed.startsWith(ROOT_PATH)) {
      setValidationError({ key: "web.admin.common.folderBrowser.absolutePath" });
      return;
    }

    try {
      setValidationError("");
      const nextFolder = await queryClient.fetchQuery({
        queryKey: adminKeys.filesystemBrowse(trimmed),
        queryFn: () => fetchFilesystemBrowse(trimmed),
        staleTime: 60_000,
      });
      setCurrentPath(nextFolder.path);
      setDraftPath(nextFolder.path);
    } catch (err) {
      setValidationError(
        err instanceof Error ? err.message : { key: "web.admin.common.folderBrowser.browseFailed" },
      );
    }
  }

  function toggleSelected(path: string) {
    setSelectedPaths((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  }

  function handleSelectCurrent() {
    if (alreadyAdded) {
      return;
    }

    onSelect([resolvedPath]);
    setSelectedPaths(new Set());
    onOpenChange(false);
  }

  function handleAddSelected() {
    const paths = Array.from(selectedPaths).filter((p) => !existingPaths.includes(p));
    if (paths.length === 0) return;
    onSelect(paths);
    setSelectedPaths(new Set());
    onOpenChange(false);
  }

  const validationText =
    typeof validationError === "string" ? validationError : t(validationError.key);

  // Count selected paths that aren't already on the library
  const addableSelected = Array.from(selectedPaths).filter(
    (p) => !existingPaths.includes(p),
  ).length;

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setSelectedPaths(new Set());
        onOpenChange(next);
      }}
    >
      <DialogContent className="overflow-x-hidden sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("web.admin.common.folderBrowser.title")}</DialogTitle>
        </DialogHeader>

        <div className="min-w-0 space-y-3">
          <div className="grid grid-cols-[1fr_auto] items-start gap-2">
            <PathAutocompleteInput
              value={draftPath}
              onValueChange={(value) => {
                setValidationError("");
                setDraftPath(value);
              }}
              placeholder={PATH_PLACEHOLDER}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void handleBrowseSubmit();
                }
              }}
            />
            <Button type="button" variant="outline" onClick={() => void handleBrowseSubmit()}>
              {t("web.admin.common.folderBrowser.browse")}
            </Button>
          </div>

          {validationText ? <p className="text-destructive text-sm">{validationText}</p> : null}
          {!validationText && error instanceof Error ? (
            <p className="text-destructive text-sm">{error.message}</p>
          ) : null}
          {alreadyAdded ? (
            <p className="text-muted-foreground text-sm">
              {t("web.admin.common.folderBrowser.alreadyListed")}
            </p>
          ) : null}

          <div className="border-border/60 overflow-hidden rounded-md border">
            <div className="border-border/60 flex items-center justify-between border-b px-3 py-2 text-sm">
              <div className="min-w-0 overflow-x-auto">
                <p className="font-medium">{t("web.admin.common.folderBrowser.currentFolder")}</p>
                <p className="text-muted-foreground font-mono text-xs whitespace-nowrap">
                  {resolvedPath}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={!canGoUp}
                  onClick={() => navigate(parentPath)}
                >
                  <ArrowUp className="me-1 h-3.5 w-3.5" />
                  {t("web.admin.common.folderBrowser.up")}
                </Button>
                <Button type="button" variant="ghost" size="icon" onClick={() => refetch()}>
                  <RefreshCw className={`h-3.5 w-3.5 ${isFetching ? "animate-spin" : ""}`} />
                </Button>
              </div>
            </div>

            <div className="h-72 overflow-auto">
              <div className="w-fit min-w-full p-2">
                {isLoading ? (
                  <div className="space-y-2">
                    {Array.from({ length: 6 }).map((_, index) => (
                      <Skeleton key={index} className="h-10 w-full rounded-md" />
                    ))}
                  </div>
                ) : null}

                {!isLoading && data?.entries.length === 0 ? (
                  <p className="text-muted-foreground px-2 py-6 text-sm">
                    {t("web.admin.common.folderBrowser.empty")}
                  </p>
                ) : null}

                {!isLoading &&
                  data?.entries.map((entry) => {
                    const isSelected = selectedPaths.has(entry.path);
                    const isExisting = existingPaths.includes(entry.path);

                    return (
                      <div
                        key={entry.path}
                        className={`flex items-center gap-0 rounded-md transition-colors ${
                          isSelected ? "bg-primary/10 ring-primary/30 ring-1" : "hover:bg-muted/60"
                        }`}
                      >
                        {/* Checkbox area */}
                        <button
                          type="button"
                          className="flex shrink-0 items-center justify-center px-2 py-2"
                          onClick={() => !isExisting && toggleSelected(entry.path)}
                          disabled={isExisting}
                          title={
                            isExisting
                              ? t("web.admin.common.folderBrowser.alreadyAdded")
                              : isSelected
                                ? t("web.admin.common.folderBrowser.deselect")
                                : t("web.admin.common.folderBrowser.select")
                          }
                        >
                          <div
                            className={`flex h-4 w-4 items-center justify-center rounded border transition-colors ${
                              isSelected
                                ? "border-primary bg-primary text-primary-foreground"
                                : isExisting
                                  ? "border-muted-foreground/30 bg-muted/50"
                                  : "border-muted-foreground/40 hover:border-primary/60"
                            }`}
                          >
                            {(isSelected || isExisting) && <Check className="h-3 w-3" />}
                          </div>
                        </button>

                        {/* Navigate into folder */}
                        <button
                          type="button"
                          className="flex min-w-0 flex-1 items-center gap-2 py-2 pe-2 text-start text-sm"
                          onClick={() => navigate(entry.path)}
                        >
                          <FolderOpen className="text-muted-foreground h-4 w-4 shrink-0" />
                          <span className="truncate">{entry.name}</span>
                        </button>
                      </div>
                    );
                  })}
              </div>
            </div>
          </div>
        </div>

        {browseQuery.hasNextPage && (
          <Button
            type="button"
            variant="outline"
            disabled={browseQuery.isFetchingNextPage}
            onClick={() => void browseQuery.fetchNextPage()}
          >
            {t("web.admin.common.folderBrowser.loadMore")}
          </Button>
        )}
        {browseQuery.isError && (
          <Button type="button" variant="outline" onClick={() => void browseQuery.restart()}>
            {t("web.admin.common.folderBrowser.restart")}
          </Button>
        )}
        <DialogFooter className="flex-row items-center gap-2 sm:justify-between">
          <div className="text-muted-foreground text-xs">
            {selectedPaths.size > 0
              ? t("web.admin.common.folderBrowser.selectedCount", {
                  count: selectedPaths.size,
                  count_text: String(selectedPaths.size),
                })
              : t("web.admin.common.folderBrowser.selectHint")}
          </div>
          <div className="flex gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            {addableSelected > 0 ? (
              <Button type="button" onClick={handleAddSelected}>
                {t("web.admin.common.folderBrowser.addFolders", {
                  count: addableSelected,
                  count_text: String(addableSelected),
                })}
              </Button>
            ) : (
              <Button type="button" onClick={handleSelectCurrent} disabled={alreadyAdded}>
                {t("web.admin.common.folderBrowser.useCurrent")}
              </Button>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
