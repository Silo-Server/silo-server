import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowUp, Check, FolderOpen, RefreshCw, FileArchive } from "lucide-react";

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

interface FolderBrowserProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSelect: (paths: string[]) => void;
  existingPaths?: string[];
}

const ROOT_PATH = "/";

export default function FolderBrowser({
  open,
  onOpenChange,
  onSelect,
  existingPaths = [],
}: FolderBrowserProps) {
  const [currentPath, setCurrentPath] = useState(ROOT_PATH);
  const [draftPath, setDraftPath] = useState(ROOT_PATH);
  const [validationError, setValidationError] = useState("");
  const [selectedPaths, setSelectedPaths] = useState<Set<string>>(new Set());
  const queryClient = useQueryClient();
  const debouncedDraftPath = useDebounce(draftPath.trim(), 200);
  const { data, isLoading, isFetching, error, refetch } = useFilesystemBrowse(currentPath);

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
      setValidationError("Use an absolute path that starts with /.");
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
      setValidationError(err instanceof Error ? err.message : "Failed to browse folder");
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
          <DialogTitle>Browse Library Folders</DialogTitle>
        </DialogHeader>

        <div className="min-w-0 space-y-3">
          <div className="grid grid-cols-[1fr_auto] items-start gap-2">
            <PathAutocompleteInput
              value={draftPath}
              onValueChange={(value) => {
                setValidationError("");
                setDraftPath(value);
              }}
              placeholder="/mnt/media"
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void handleBrowseSubmit();
                }
              }}
            />
            <Button type="button" variant="outline" onClick={() => void handleBrowseSubmit()}>
              Browse
            </Button>
          </div>

          {validationError ? <p className="text-destructive text-sm">{validationError}</p> : null}
          {!validationError && error instanceof Error ? (
            <p className="text-destructive text-sm">{error.message}</p>
          ) : null}
          {alreadyAdded ? (
            <p className="text-muted-foreground text-sm">
              This folder is already listed on the library.
            </p>
          ) : null}

          <div className="border-border/60 overflow-hidden rounded-md border">
            <div className="border-border/60 flex items-center justify-between border-b px-3 py-2 text-sm">
              <div className="min-w-0 overflow-x-auto">
                <p className="font-medium">Current folder</p>
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
                  <ArrowUp className="mr-1 h-3.5 w-3.5" />
                  Up
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
                    No subfolders found here.
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
                          title={isExisting ? "Already added" : isSelected ? "Deselect" : "Select"}
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

                        <button
                          type="button"
                          className="flex min-w-0 flex-1 items-center gap-2 py-2 pr-2 text-left text-sm"
                          onClick={() => {
                            if (entry.is_dir === false) {
                              if (!isExisting) {
                                toggleSelected(entry.path);
                              }
                            } else {
                              navigate(entry.path);
                            }
                          }}
                        >
                          {entry.is_dir === false ? (
                            <FileArchive className="text-muted-foreground h-4 w-4 shrink-0" />
                          ) : (
                            <FolderOpen className="text-muted-foreground h-4 w-4 shrink-0" />
                          )}
                          <span className="truncate">{entry.name}</span>
                        </button>
                      </div>
                    );
                  })}
              </div>
            </div>
          </div>
        </div>

        <DialogFooter className="flex-row items-center gap-2 sm:justify-between">
          <div className="text-muted-foreground text-xs">
            {selectedPaths.size > 0
              ? `${selectedPaths.size} folder${selectedPaths.size !== 1 ? "s" : ""} selected`
              : "Select folders or use current"}
          </div>
          <div className="flex gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            {addableSelected > 0 ? (
              <Button type="button" onClick={handleAddSelected}>
                Add {addableSelected} Folder{addableSelected !== 1 ? "s" : ""}
              </Button>
            ) : (
              <Button type="button" onClick={handleSelectCurrent} disabled={alreadyAdded}>
                Use Current Folder
              </Button>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
