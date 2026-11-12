import { useMemo, useState } from "react";
import { Link } from "react-router";
import { AlertTriangle, FolderOpen, XIcon } from "lucide-react";

import type { Library, LibraryRoot } from "@/api/types";
import { CollapsibleDiagnosticsSection } from "@/components/admin/CollapsibleDiagnosticsSection";
import {
  CARD_ACTION_CLASS,
  CardActions,
  CardField,
  CardFields,
  CardListMessage,
  CardPath,
  DiagnosticsCard,
  DiagnosticsCardList,
  DiagnosticsSearch,
  PaginationBar,
  TOUCH_CONTROL_CLASS,
} from "@/components/admin/libraries/DiagnosticsListParts";
import {
  pathLeaf,
  useLibrariesTableLayout,
  usePagination,
} from "@/components/admin/libraries/diagnosticsListState";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  flattenLibraryRoots,
  useDeleteLibraryRootOverride,
  useLibraryRoots,
  useUpsertLibraryRootOverride,
} from "@/hooks/queries/admin/libraries";
import { useDebounce } from "@/hooks/useDebounce";
import { useT, type Translate } from "@/i18n";
import { cn } from "@/lib/utils";

// Not translated: a sample year.
const YEAR_PLACEHOLDER = "2024";

/** What the table row and the card both show for one root. */
function rootRowModel(t: Translate, root: LibraryRoot) {
  return {
    key: `${root.library_id}:${root.root_path}`,
    label: root.title || pathLeaf(root.root_path),
    evidence: root.evidence_json ? buildRootEvidenceSummary(t, root) : null,
    typeLabel: root.inferred_type || t("web.admin.libraries.ambiguousRoots.unknownType"),
  };
}

/**
 * AmbiguousRootsSection displays scanner roots that require manual resolution,
 * handling loading, confirmed empty, populated warning, and error states.
 */
export function AmbiguousRootsSection({ libraries }: { libraries: Library[] }) {
  const t = useT();
  const showTable = useLibrariesTableLayout();
  const [open, setOpen] = useState(false);
  const [selectedLibraryId, setSelectedLibraryId] = useState<number | undefined>(libraries[0]?.id);
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebounce(search, 250);
  const [editingRoot, setEditingRoot] = useState<LibraryRoot | null>(null);
  const effectiveSelectedLibraryId = selectedLibraryId ?? libraries[0]?.id;
  // The listing is paged on demand: the first page loads when the section
  // opens and "Load more" fetches the rest, so a large library does not make
  // the server walk every root on mount. The search is server-side and spans
  // every page, so a root on a later page is found without loading it; a new
  // search term restarts paging from the first page.
  const {
    data: rootPages,
    isError,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = useLibraryRoots(effectiveSelectedLibraryId, "ambiguous", {
    enabled: open,
    search: debouncedSearch,
  });
  const roots = useMemo(() => flattenLibraryRoots(rootPages), [rootPages]);
  const totalRoots = rootPages?.pages[0]?.total ?? 0;
  // Until the first page arrives for the selected library and search, the
  // count is unknown: the query is disabled while collapsed, so a missing
  // page must not read as a confirmed zero. A failure after a page loaded
  // (a later page or a refetch) keeps showing what already loaded.
  const loadFailed = rootPages === undefined && isError;
  const countUnknown = rootPages === undefined && !isError;
  const isWarning = totalRoots > 0;

  const pag = usePagination(roots);

  if (libraries.length === 0) {
    return null;
  }

  // The line shown instead of rows while loading, after a failure, or when empty.
  const listMessage: { tone: "muted" | "error"; text: string } | null = loadFailed
    ? { tone: "error", text: t("web.admin.libraries.ambiguousRoots.loadFailed") }
    : countUnknown
      ? { tone: "muted", text: t("web.admin.libraries.ambiguousRoots.loading") }
      : roots.length === 0
        ? {
            tone: "muted",
            text: debouncedSearch.trim()
              ? t("web.admin.libraries.ambiguousRoots.noMatches")
              : t("web.admin.libraries.ambiguousRoots.empty"),
          }
        : null;

  return (
    <CollapsibleDiagnosticsSection
      title={t("web.admin.libraries.ambiguousRoots.title")}
      description={t("web.admin.libraries.ambiguousRoots.description")}
      count={countUnknown ? undefined : totalRoots}
      isError={loadFailed}
      icon={
        loadFailed ? (
          <AlertTriangle className="text-destructive h-4 w-4" />
        ) : (
          <FolderOpen
            className={cn("h-4 w-4", isWarning ? "text-amber-500" : "text-muted-foreground")}
          />
        )
      }
      iconClassName={
        loadFailed ? "bg-destructive/10" : isWarning ? "bg-amber-500/10" : "bg-muted/50"
      }
      open={open}
      onOpenChange={setOpen}
    >
      <div className="mb-2 flex flex-col gap-2 sm:flex-row">
        <Select
          value={
            effectiveSelectedLibraryId != null ? String(effectiveSelectedLibraryId) : undefined
          }
          onValueChange={(value) => {
            setSelectedLibraryId(Number.parseInt(value, 10));
            pag.setPage(0);
          }}
        >
          <SelectTrigger
            className={cn("h-8 w-full text-xs sm:w-[220px]", TOUCH_CONTROL_CLASS, "max-lg:text-sm")}
          >
            <SelectValue placeholder={t("web.admin.libraries.ambiguousRoots.selectLibrary")} />
          </SelectTrigger>
          <SelectContent>
            {libraries.map((library) => (
              <SelectItem key={library.id} value={String(library.id)} className="max-lg:min-h-11">
                {library.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <DiagnosticsSearch
          className="flex-1"
          placeholder={t("web.admin.libraries.ambiguousRoots.filterPlaceholder")}
          value={search}
          onValueChange={(value) => {
            setSearch(value);
            pag.setPage(0);
          }}
        />
      </div>

      {showTable ? (
        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>{t("web.admin.libraries.column.root")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.type")}</TableHead>
                <TableHead>{t("web.admin.libraries.column.confidence")}</TableHead>
                <TableHead className="text-end">{t("web.admin.libraries.column.files")}</TableHead>
                <TableHead className="w-[120px]">
                  {t("web.admin.libraries.column.actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {listMessage ? (
                <TableRow>
                  <TableCell
                    colSpan={5}
                    className={cn(
                      "text-center text-sm",
                      listMessage.tone === "error" ? "text-destructive" : "text-muted-foreground",
                    )}
                  >
                    {listMessage.text}
                  </TableCell>
                </TableRow>
              ) : (
                pag.rows.map((root) => {
                  const row = rootRowModel(t, root);
                  return (
                    <TableRow key={row.key}>
                      <TableCell className="max-w-[28rem]">
                        <div className="space-y-1">
                          <div className="truncate text-sm font-medium">{row.label}</div>
                          <code className="text-muted-foreground block truncate text-[11px]">
                            {root.root_path}
                          </code>
                          {row.evidence !== null ? (
                            <div className="text-muted-foreground truncate text-[11px]">
                              {row.evidence}
                            </div>
                          ) : null}
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">{row.typeLabel}</Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary">{root.type_confidence}</Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-end text-xs tabular-nums">
                        {root.observed_file_count}
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <AmbiguousRootActions
                            root={root}
                            onOverride={() => setEditingRoot(root)}
                            buttonClassName="h-7 text-xs"
                          />
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })
              )}
            </TableBody>
          </Table>
        </div>
      ) : listMessage ? (
        <CardListMessage tone={listMessage.tone}>{listMessage.text}</CardListMessage>
      ) : (
        <DiagnosticsCardList>
          {pag.rows.map((root) => {
            const row = rootRowModel(t, root);
            return (
              <DiagnosticsCard key={row.key}>
                <div className="space-y-1">
                  <h3 className="font-medium break-words">{row.label}</h3>
                  <CardPath path={root.root_path} />
                </div>
                <CardFields>
                  <CardField label={t("web.admin.libraries.column.type")}>
                    <Badge variant="outline" className="max-w-full whitespace-normal">
                      {row.typeLabel}
                    </Badge>
                  </CardField>
                  <CardField label={t("web.admin.libraries.column.confidence")}>
                    <Badge variant="secondary" className="max-w-full whitespace-normal">
                      {root.type_confidence}
                    </Badge>
                  </CardField>
                  <CardField label={t("web.admin.libraries.column.files")}>
                    <span className="tabular-nums">{root.observed_file_count}</span>
                  </CardField>
                </CardFields>
                {row.evidence ? (
                  <p className="text-muted-foreground text-xs break-words">{row.evidence}</p>
                ) : null}
                <CardActions>
                  <AmbiguousRootActions
                    root={root}
                    onOverride={() => setEditingRoot(root)}
                    buttonClassName={CARD_ACTION_CLASS}
                  />
                </CardActions>
              </DiagnosticsCard>
            );
          })}
        </DiagnosticsCardList>
      )}
      <PaginationBar {...pag} />
      {hasNextPage ? (
        <div className="mt-2 flex flex-wrap items-center justify-between gap-3 text-xs">
          <span className="text-muted-foreground tabular-nums">
            {t("web.admin.libraries.ambiguousRoots.showing", {
              count: totalRoots,
              shown: roots.length,
              count_text: totalRoots,
            })}
          </span>
          <Button
            variant="outline"
            size="sm"
            className={cn("h-7 text-xs", TOUCH_CONTROL_CLASS)}
            disabled={isFetchingNextPage}
            onClick={() => {
              void fetchNextPage();
            }}
          >
            {isFetchingNextPage
              ? t("web.admin.libraries.loadingMore")
              : t("web.admin.libraries.loadMore")}
          </Button>
        </div>
      ) : null}

      {editingRoot ? (
        <RootOverrideDialog
          key={`${editingRoot.library_id}:${editingRoot.root_path}`}
          root={editingRoot}
          onOpenChange={(open) => {
            if (!open) setEditingRoot(null);
          }}
        />
      ) : null}
    </CollapsibleDiagnosticsSection>
  );
}

/** A root's Override and Resolve actions, sized by the layout that shows them. */
function AmbiguousRootActions({
  root,
  onOverride,
  buttonClassName,
}: {
  root: LibraryRoot;
  onOverride: () => void;
  buttonClassName: string;
}) {
  const t = useT();
  return (
    <>
      <Button variant="outline" size="sm" className={buttonClassName} onClick={onOverride}>
        {t("web.admin.libraries.ambiguousRoots.override")}
      </Button>
      {root.content_id ? (
        <Button
          variant="outline"
          size="sm"
          className={buttonClassName}
          asChild
          title={t("web.admin.libraries.ambiguousRoots.resolveTitle")}
        >
          <Link to={`/item/${encodeURIComponent(root.content_id)}`}>
            {t("web.admin.libraries.ambiguousRoots.resolve")}
          </Link>
        </Button>
      ) : null}
    </>
  );
}

function buildRootEvidenceSummary(t: Translate, root: LibraryRoot): string {
  const evidence = root.evidence_json ?? {};
  const parts: string[] = [];

  if (typeof evidence.has_folder_ids === "boolean") {
    parts.push(
      evidence.has_folder_ids
        ? t("web.admin.libraries.ambiguousRoots.evidence.folderIds")
        : t("web.admin.libraries.ambiguousRoots.evidence.noFolderIds"),
    );
  }
  if (typeof evidence.season_structure_files === "number" && evidence.season_structure_files > 0) {
    parts.push(
      t("web.admin.libraries.ambiguousRoots.evidence.seasonFiles", {
        count: evidence.season_structure_files,
        count_text: evidence.season_structure_files,
      }),
    );
  }
  if (typeof evidence.movie_evidence_files === "number" && evidence.movie_evidence_files > 0) {
    parts.push(
      t("web.admin.libraries.ambiguousRoots.evidence.movieFiles", {
        count: evidence.movie_evidence_files,
        count_text: evidence.movie_evidence_files,
      }),
    );
  }
  if (typeof evidence.wrapper_collapses === "number" && evidence.wrapper_collapses > 0) {
    parts.push(
      t("web.admin.libraries.ambiguousRoots.evidence.wrapperCollapses", {
        count: evidence.wrapper_collapses,
        count_text: evidence.wrapper_collapses,
      }),
    );
  }
  if (typeof evidence.ancestor_promotions === "number" && evidence.ancestor_promotions > 0) {
    parts.push(
      t("web.admin.libraries.ambiguousRoots.evidence.ancestorPromotions", {
        count: evidence.ancestor_promotions,
        count_text: evidence.ancestor_promotions,
      }),
    );
  }

  return parts.join(" · ");
}

// Fields and buttons at least 44px tall below lg.
const DIALOG_CONTROL_CLASS = "max-lg:h-11";

function RootOverrideDialog({
  root,
  onOpenChange,
}: {
  root: LibraryRoot;
  onOpenChange: (open: boolean) => void;
}) {
  const t = useT();
  const upsertOverride = useUpsertLibraryRootOverride();
  const deleteOverride = useDeleteLibraryRootOverride();
  const [forcedType, setForcedType] = useState(
    root.active_override?.forced_type || root.inferred_type || "",
  );
  const [forcedTitle, setForcedTitle] = useState(
    root.active_override?.forced_title || root.title || "",
  );
  const [forcedYear, setForcedYear] = useState(
    root.active_override?.forced_year
      ? String(root.active_override.forced_year)
      : root.year
        ? String(root.year)
        : "",
  );
  const [forcedTmdbID, setForcedTmdbID] = useState(
    root.active_override?.forced_tmdb_id || root.tmdb_id || "",
  );
  const [forcedImdbID, setForcedImdbID] = useState(
    root.active_override?.forced_imdb_id || root.imdb_id || "",
  );
  const [forcedTvdbID, setForcedTvdbID] = useState(
    root.active_override?.forced_tvdb_id || root.tvdb_id || "",
  );
  const [note, setNote] = useState(root.active_override?.note || "");

  return (
    <Dialog open={true} onOpenChange={onOpenChange}>
      {/* Below sm the dialog fills the screen with a fixed header and footer;
          from sm up it is the default centered box. */}
      <DialogContent
        className={cn(
          "max-sm:inset-0 max-sm:flex max-sm:h-dvh max-sm:max-h-none max-sm:w-dvw max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:flex-col max-sm:gap-0 max-sm:overflow-hidden max-sm:rounded-none max-sm:border-0 max-sm:p-0",
          // The header carries its own 44px close button on a phone.
          "max-sm:[&>[data-slot=dialog-close]]:hidden",
        )}
      >
        <DialogHeader className="max-sm:border-border max-sm:shrink-0 max-sm:border-b max-sm:ps-4 max-sm:pe-2 max-sm:pt-[calc(0.75rem+env(safe-area-inset-top))] max-sm:pb-3">
          <div className="flex items-start gap-3">
            <div className="flex min-w-0 flex-1 flex-col gap-2 max-sm:text-start">
              <DialogTitle>{t("web.admin.libraries.rootOverride.title")}</DialogTitle>
              {/* The fixed phone header holds the full root path; a long one
                  scrolls here instead of squeezing the form below. */}
              <DialogDescription className="break-words max-sm:max-h-[25dvh] max-sm:overflow-y-auto">
                {t("web.admin.libraries.rootOverride.description", {
                  path: root.root_path,
                  code: (chunks) => <code className="break-all">{chunks}</code>,
                })}
              </DialogDescription>
            </div>
            <DialogClose asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground -mt-1.5 size-11 shrink-0 sm:hidden"
              >
                <XIcon className="size-5" />
                <span className="sr-only">{t("web.ui.close")}</span>
              </Button>
            </DialogClose>
          </div>
        </DialogHeader>

        <div className="space-y-4 max-sm:min-h-0 max-sm:flex-1 max-sm:overflow-y-auto max-sm:px-4 max-sm:py-5">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label>{t("web.admin.libraries.rootOverride.type")}</Label>
              <Select
                value={forcedType || "auto"}
                onValueChange={(value) => setForcedType(value === "auto" ? "" : value)}
              >
                <SelectTrigger className={cn("max-sm:w-full", DIALOG_CONTROL_CLASS)}>
                  <SelectValue placeholder={t("web.admin.libraries.rootOverride.auto")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto" className="max-lg:min-h-11">
                    {t("web.admin.libraries.rootOverride.auto")}
                  </SelectItem>
                  <SelectItem value="movie" className="max-lg:min-h-11">
                    {t("web.admin.libraries.rootOverride.movie")}
                  </SelectItem>
                  <SelectItem value="series" className="max-lg:min-h-11">
                    {t("web.admin.libraries.rootOverride.series")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>{t("web.admin.libraries.rootOverride.year")}</Label>
              <Input
                className={DIALOG_CONTROL_CLASS}
                value={forcedYear}
                onChange={(e) => setForcedYear(e.target.value)}
                placeholder={YEAR_PLACEHOLDER}
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <Label>{t("web.admin.libraries.rootOverride.titleLabel")}</Label>
            <Input
              className={DIALOG_CONTROL_CLASS}
              value={forcedTitle}
              onChange={(e) => setForcedTitle(e.target.value)}
              placeholder={t("web.admin.libraries.rootOverride.titlePlaceholder")}
            />
          </div>

          <div className="grid gap-3 sm:grid-cols-3">
            <div className="space-y-1.5">
              <Label>{t("web.admin.libraries.rootOverride.tmdbId")}</Label>
              <Input
                className={DIALOG_CONTROL_CLASS}
                value={forcedTmdbID}
                onChange={(e) => setForcedTmdbID(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label>{t("web.admin.libraries.rootOverride.imdbId")}</Label>
              <Input
                className={DIALOG_CONTROL_CLASS}
                value={forcedImdbID}
                onChange={(e) => setForcedImdbID(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label>{t("web.admin.libraries.rootOverride.tvdbId")}</Label>
              <Input
                className={DIALOG_CONTROL_CLASS}
                value={forcedTvdbID}
                onChange={(e) => setForcedTvdbID(e.target.value)}
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <Label>{t("web.admin.libraries.rootOverride.note")}</Label>
            <Input
              className={DIALOG_CONTROL_CLASS}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={t("web.admin.libraries.rootOverride.notePlaceholder")}
            />
          </div>
        </div>

        <div className="flex justify-between gap-2 max-sm:shrink-0 max-sm:border-t max-sm:px-4 max-sm:pt-3 max-sm:pb-[calc(0.75rem+env(safe-area-inset-bottom))]">
          <Button
            variant="outline"
            className={cn("max-sm:flex-1", DIALOG_CONTROL_CLASS)}
            disabled={!root.active_override || deleteOverride.isPending}
            onClick={() => {
              deleteOverride.mutate(
                { library_id: root.library_id, root_path: root.root_path },
                { onSuccess: () => onOpenChange(false) },
              );
            }}
          >
            {t("web.admin.libraries.rootOverride.remove")}
          </Button>
          <Button
            className={cn("max-sm:flex-1", DIALOG_CONTROL_CLASS)}
            onClick={() => {
              const parsedYear = Number.parseInt(forcedYear, 10);
              upsertOverride.mutate(
                {
                  library_id: root.library_id,
                  root_path: root.root_path,
                  forced_type: forcedType || undefined,
                  forced_title: forcedTitle || undefined,
                  forced_year: Number.isFinite(parsedYear) ? parsedYear : undefined,
                  forced_tmdb_id: forcedTmdbID || undefined,
                  forced_imdb_id: forcedImdbID || undefined,
                  forced_tvdb_id: forcedTvdbID || undefined,
                  note: note || undefined,
                },
                { onSuccess: () => onOpenChange(false) },
              );
            }}
            disabled={upsertOverride.isPending}
          >
            {t("web.admin.libraries.rootOverride.save")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
