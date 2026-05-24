import { useMemo, useState } from "react";
import { ChevronLeft, Layers3, Search } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { Library } from "@/api/types";
import {
  libraryEligibilityForMediaKind,
  useCollectionTemplateBundles,
  useCollectionTemplates,
  type ApplyCollectionTemplateBundleFeaturedRequest,
  type ApplyCollectionTemplateBundleResponse,
  type CollectionTemplate,
  type CollectionTemplateBundle,
  type CollectionTemplateCategory,
  type CollectionTemplateGroup,
} from "@/lib/collectionTemplates";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import {
  useApplyCollectionTemplateBundle,
  useQueueCollectionTemplateBundleApply,
} from "@/hooks/queries/admin/collections";
import { useUserCollectionTemplates } from "@/hooks/queries/userCollectionImports";
import { CollectionTemplateCard } from "./CollectionTemplateCard";
import { CollectionTemplateConfigForm } from "./CollectionTemplateConfigForm";
import { UserCollectionTemplateConfigForm } from "./UserCollectionTemplateConfigForm";

type ActiveCategory = CollectionTemplateCategory | "all";

interface AdminProps {
  mode?: "admin";
  open: boolean;
  onOpenChange: (open: boolean) => void;
  libraries: Library[];
  initialLibraryId: number | null;
  /** Optional callback fired once a template-driven import succeeds. */
  onCreated?: () => void;
}

interface UserProps {
  mode: "user";
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Optional callback fired once a template-driven import succeeds. */
  onCreated?: () => void;
}

type Props = AdminProps | UserProps;

export function CollectionTemplateGallery(props: Props) {
  const isUserMode = props.mode === "user";
  const adminTemplates = useCollectionTemplates(!isUserMode && props.open);
  const adminBundles = useCollectionTemplateBundles(!isUserMode && props.open);
  const userTemplates = useUserCollectionTemplates(isUserMode && props.open);
  const { data, isLoading, error } = isUserMode ? userTemplates : adminTemplates;
  const galleryError = !isUserMode && adminBundles.error ? adminBundles.error : error;
  const { open, onOpenChange, onCreated } = props;
  const [search, setSearch] = useState("");
  const [activeCategory, setActiveCategory] = useState<ActiveCategory>("all");
  const [picked, setPicked] = useState<CollectionTemplate | null>(null);
  const [pickedBundle, setPickedBundle] = useState<CollectionTemplateBundle | null>(null);

  const handleOpenChange = (next: boolean) => {
    // Reset internal state on close so the next open starts at the gallery
    // root rather than resuming a stale picked-template view.
    if (!next) {
      setSearch("");
      setActiveCategory("all");
      setPicked(null);
      setPickedBundle(null);
    }
    onOpenChange(next);
  };

  const groups: CollectionTemplateGroup[] = useMemo(() => data?.categories ?? [], [data]);
  const bundles = !isUserMode ? (adminBundles.data?.bundles ?? []) : [];

  const filtered = useMemo(() => {
    const term = search.trim().toLowerCase();
    return groups
      .filter((group) => activeCategory === "all" || group.category === activeCategory)
      .map((group) => ({
        ...group,
        templates: group.templates.filter((tmpl) => {
          if (!term) return true;
          return (
            tmpl.title.toLowerCase().includes(term) ||
            tmpl.description.toLowerCase().includes(term) ||
            (tmpl.tags ?? []).some((tag) => tag.toLowerCase().includes(term))
          );
        }),
      }))
      .filter((group) => group.templates.length > 0);
  }, [groups, search, activeCategory]);

  const totalMatches = filtered.reduce((sum, group) => sum + group.templates.length, 0);

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-3xl lg:max-w-4xl">
        <DialogHeader className="space-y-1">
          <DialogTitle className="flex items-center gap-2">
            {picked || pickedBundle ? (
              <button
                type="button"
                onClick={() => {
                  setPicked(null);
                  setPickedBundle(null);
                }}
                className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm font-medium"
              >
                <ChevronLeft className="h-4 w-4" /> Back
              </button>
            ) : (
              "Browse Collection Templates"
            )}
          </DialogTitle>
          <DialogDescription>
            {picked
              ? "Confirm details, then we'll create and sync the collection for you."
              : pickedBundle
                ? "Choose libraries, preview the defaults, then apply the bundle."
                : "Pick a curated source — TMDB, Trakt, or MDBList — and we'll seed a synced collection."}
          </DialogDescription>
        </DialogHeader>

        {picked ? (
          isUserMode ? (
            <UserCollectionTemplateConfigForm
              key={picked.id}
              template={picked}
              onCancel={() => setPicked(null)}
              onCreated={() => {
                handleOpenChange(false);
                onCreated?.();
              }}
            />
          ) : (
            <CollectionTemplateConfigForm
              key={picked.id}
              template={picked}
              libraries={(props as AdminProps).libraries}
              initialLibraryId={(props as AdminProps).initialLibraryId}
              onCancel={() => setPicked(null)}
              onCreated={() => {
                handleOpenChange(false);
                onCreated?.();
              }}
            />
          )
        ) : pickedBundle && !isUserMode ? (
          <TemplateBundleApplyView
            bundle={pickedBundle}
            libraries={(props as AdminProps).libraries}
            initialLibraryId={(props as AdminProps).initialLibraryId}
            groups={groups}
            onApplied={() => {
              handleOpenChange(false);
              onCreated?.();
            }}
          />
        ) : (
          <GalleryView
            isLoading={isLoading}
            error={galleryError}
            bundles={bundles}
            groups={groups}
            filtered={filtered}
            totalMatches={totalMatches}
            search={search}
            setSearch={setSearch}
            activeCategory={activeCategory}
            setActiveCategory={setActiveCategory}
            onPick={setPicked}
            onPickBundle={setPickedBundle}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

interface GalleryViewProps {
  isLoading: boolean;
  error: unknown;
  bundles: CollectionTemplateBundle[];
  groups: CollectionTemplateGroup[];
  filtered: CollectionTemplateGroup[];
  totalMatches: number;
  search: string;
  setSearch: (value: string) => void;
  activeCategory: ActiveCategory;
  setActiveCategory: (value: ActiveCategory) => void;
  onPick: (template: CollectionTemplate) => void;
  onPickBundle: (bundle: CollectionTemplateBundle) => void;
}

function GalleryView({
  isLoading,
  error,
  bundles,
  groups,
  filtered,
  totalMatches,
  search,
  setSearch,
  activeCategory,
  setActiveCategory,
  onPick,
  onPickBundle,
}: GalleryViewProps) {
  if (error) {
    return (
      <div className="text-destructive rounded-md border border-red-500/40 bg-red-500/10 p-3 text-sm">
        Failed to load templates: {error instanceof Error ? error.message : String(error)}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {bundles.length > 0 ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {bundles.map((bundle) => (
            <button
              key={bundle.id}
              type="button"
              onClick={() => onPickBundle(bundle)}
              className="border-border hover:border-primary/60 bg-muted/30 flex min-h-24 items-start gap-3 rounded-md border p-3 text-left transition-colors"
            >
              <Layers3 className="text-primary mt-0.5 h-4 w-4 shrink-0" />
              <span className="space-y-1">
                <span className="block text-sm font-semibold">{bundle.title}</span>
                <span className="text-muted-foreground block text-xs leading-snug">
                  {bundle.description}
                </span>
                <span className="text-muted-foreground block text-xs">
                  {bundle.template_ids.length} templates
                </span>
              </span>
            </button>
          ))}
        </div>
      ) : null}

      <div className="relative">
        <Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
        <Input
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder="Search templates"
          className="pl-9"
        />
      </div>

      <div className="-mx-1 flex flex-wrap gap-2 px-1">
        <CategoryPill
          label="All"
          active={activeCategory === "all"}
          onClick={() => setActiveCategory("all")}
        />
        {groups.map((group) => (
          <CategoryPill
            key={group.category}
            label={group.label}
            active={activeCategory === group.category}
            onClick={() => setActiveCategory(group.category)}
          />
        ))}
      </div>

      {isLoading ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 6 }).map((_, idx) => (
            <Skeleton key={idx} className="h-36 w-full rounded-2xl" />
          ))}
        </div>
      ) : totalMatches === 0 ? (
        <div className="text-muted-foreground rounded-md border border-dashed py-10 text-center text-sm">
          No templates match your filters.
        </div>
      ) : (
        <div className="space-y-6">
          {filtered.map((group) => (
            <section key={group.category} className="space-y-2">
              <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
                {group.label}
              </h3>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {group.templates.map((tmpl) => (
                  <CollectionTemplateCard key={tmpl.id} template={tmpl} onPick={onPick} />
                ))}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}

function TemplateBundleApplyView({
  bundle,
  libraries,
  initialLibraryId,
  groups,
  onApplied,
}: {
  bundle: CollectionTemplateBundle;
  libraries: Library[];
  initialLibraryId: number | null;
  groups: CollectionTemplateGroup[];
  onApplied?: () => void;
}) {
  const initialIds = useMemo(() => {
    if (initialLibraryId) return [initialLibraryId];
    return libraries.map((library) => library.id);
  }, [initialLibraryId, libraries]);
  const templatesById = useMemo(() => {
    const map = new Map<string, CollectionTemplate>();
    groups.forEach((group) =>
      group.templates.forEach((template) => map.set(template.id, template)),
    );
    return map;
  }, [groups]);
  const [libraryIds, setLibraryIds] = useState<number[]>(initialIds);
  const [deleteExisting, setDeleteExisting] = useState(false);
  const [homeFeatured, setHomeFeatured] = useState<string | null>(null);
  const [libraryFeatured, setLibraryFeatured] = useState<Record<number, string>>({});
  const [result, setResult] = useState<ApplyCollectionTemplateBundleResponse | null>(null);
  const applyBundle = useApplyCollectionTemplateBundle();
  const queueBundleApply = useQueueCollectionTemplateBundleApply();
  const disabled = libraryIds.length === 0 || applyBundle.isPending || queueBundleApply.isPending;
  const queuesInitialSyncs = bundle.id === "all_defaults";
  const selectedLibraries = useMemo(
    () => libraries.filter((library) => libraryIds.includes(library.id)),
    [libraries, libraryIds],
  );
  const effectiveLibraryFeatured = useMemo(() => {
    const next: Record<number, string> = {};
    selectedLibraries.forEach((library) => {
      const previous = libraryFeatured[library.id];
      if (previous === "none") {
        next[library.id] = previous;
        return;
      }
      if (
        previous !== undefined &&
        isBundleTemplateEligibleForLibrary(templatesById.get(previous), library)
      ) {
        next[library.id] = previous;
        return;
      }
      next[library.id] = defaultFeaturedTemplateId(bundle, templatesById, library);
    });
    return next;
  }, [bundle, libraryFeatured, selectedLibraries, templatesById]);
  const effectiveHomeFeatured = useMemo(() => {
    if (homeFeatured === "none") return homeFeatured;
    if (homeFeatured) {
      const parsed = parseHomeFeaturedValue(homeFeatured);
      const library = selectedLibraries.find((item) => item.id === parsed?.libraryId);
      const template = parsed ? templatesById.get(parsed.templateId) : undefined;
      if (library && isBundleTemplateEligibleForLibrary(template, library)) {
        return homeFeatured;
      }
    }
    return defaultHomeFeaturedValue(bundle, templatesById, selectedLibraries);
  }, [bundle, homeFeatured, selectedLibraries, templatesById]);

  const preview = () => {
    const featured = buildFeaturedBundleRequest(effectiveHomeFeatured, effectiveLibraryFeatured);
    applyBundle.mutate(
      {
        bundleId: bundle.id,
        body: {
          library_ids: libraryIds,
          dry_run: true,
          delete_existing: deleteExisting,
          featured,
        },
      },
      {
        onSuccess: (nextResult) => {
          setResult(nextResult);
        },
      },
    );
  };

  const apply = () => {
    const featured = buildFeaturedBundleRequest(effectiveHomeFeatured, effectiveLibraryFeatured);
    queueBundleApply.mutate(
      {
        bundleId: bundle.id,
        body: {
          library_ids: libraryIds,
          delete_existing: deleteExisting,
          featured,
        },
      },
      {
        onSuccess: () => onApplied?.(),
      },
    );
  };

  return (
    <div className="space-y-4">
      <div className="rounded-md border p-3">
        <div className="text-sm font-semibold">{bundle.title}</div>
        <p className="text-muted-foreground mt-1 text-xs leading-snug">{bundle.description}</p>
        {queuesInitialSyncs ? (
          <p className="text-muted-foreground mt-2 text-xs leading-snug">
            Collections are created first; initial syncs are queued so this large bundle can finish
            without waiting on every source.
          </p>
        ) : null}
      </div>

      <div className="space-y-2">
        <div className="text-sm font-medium">Libraries</div>
        <LibraryMultiSelect
          libraries={libraries}
          value={libraryIds}
          onChange={setLibraryIds}
          emptyLabel="Choose libraries"
          hideAllOption
        />
      </div>

      <div className="space-y-3 rounded-md border p-3">
        <div>
          <div className="text-sm font-semibold">Featured Sections</div>
          <p className="text-muted-foreground mt-1 text-xs leading-snug">
            Create one hero section for Home and one for each selected library.
          </p>
        </div>

        <div className="space-y-2">
          <Label>Home Hero</Label>
          <Select value={effectiveHomeFeatured} onValueChange={setHomeFeatured}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="none">No home hero</SelectItem>
              {selectedLibraries.flatMap((library) =>
                eligibleBundleTemplates(bundle, templatesById, library).map((template) => (
                  <SelectItem
                    key={`${library.id}:${template.id}`}
                    value={`${library.id}:${template.id}`}
                  >
                    {library.name} / {template.title}
                  </SelectItem>
                )),
              )}
            </SelectContent>
          </Select>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          {selectedLibraries.map((library) => (
            <div key={library.id} className="space-y-2">
              <Label>{library.name}</Label>
              <Select
                value={effectiveLibraryFeatured[library.id] ?? "none"}
                onValueChange={(value) =>
                  setLibraryFeatured((prev) => ({ ...prev, [library.id]: value }))
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">No library hero</SelectItem>
                  {eligibleBundleTemplates(bundle, templatesById, library).map((template) => (
                    <SelectItem key={template.id} value={template.id}>
                      {template.title}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ))}
        </div>
      </div>

      <label className="flex items-center justify-between gap-3 rounded-md border p-3">
        <span className="space-y-1">
          <span className="block text-sm font-medium">Delete Existing Server Collections</span>
          <span className="text-muted-foreground block text-xs">
            Remove current server collections in the selected libraries before applying defaults.
          </span>
        </span>
        <Switch checked={deleteExisting} onCheckedChange={setDeleteExisting} />
      </label>

      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" disabled={disabled} onClick={preview}>
          Preview
        </Button>
        <Button type="button" disabled={disabled} onClick={apply}>
          {queueBundleApply.isPending ? "Queueing..." : "Apply Defaults"}
        </Button>
      </div>

      {result ? <BundleApplyResult result={result} /> : null}
    </div>
  );
}

function eligibleBundleTemplates(
  bundle: CollectionTemplateBundle,
  templatesById: Map<string, CollectionTemplate>,
  library: Library,
): CollectionTemplate[] {
  return bundle.template_ids
    .map((id) => templatesById.get(id))
    .filter((template): template is CollectionTemplate =>
      isBundleTemplateEligibleForLibrary(template, library),
    );
}

function isBundleTemplateEligibleForLibrary(
  template: CollectionTemplate | undefined,
  library: Library,
): template is CollectionTemplate {
  if (!template) return false;
  const eligibility = libraryEligibilityForMediaKind(template.media_kind);
  if (!eligibility.kinds || eligibility.kinds.length === 0) return true;
  if (!library.type || library.type === "mixed") return true;
  return eligibility.kinds.includes(library.type);
}

function defaultFeaturedTemplateId(
  bundle: CollectionTemplateBundle,
  templatesById: Map<string, CollectionTemplate>,
  library: Library,
): string {
  for (const id of [
    "tmdb_trending_movies_week",
    "tmdb_trending_tv_week",
    "tmdb_popular_movies",
    "tmdb_popular_tv",
  ]) {
    if (!bundle.template_ids.includes(id)) continue;
    const template = templatesById.get(id);
    if (isBundleTemplateEligibleForLibrary(template, library)) {
      return id;
    }
  }
  return eligibleBundleTemplates(bundle, templatesById, library)[0]?.id ?? "none";
}

function defaultHomeFeaturedValue(
  bundle: CollectionTemplateBundle,
  templatesById: Map<string, CollectionTemplate>,
  libraries: Library[],
): string {
  for (const id of ["tmdb_trending_movies_week", "tmdb_trending_tv_week"]) {
    if (!bundle.template_ids.includes(id)) continue;
    const library = libraries.find((candidate) =>
      isBundleTemplateEligibleForLibrary(templatesById.get(id), candidate),
    );
    if (library) return `${library.id}:${id}`;
  }
  for (const library of libraries) {
    const templateID = defaultFeaturedTemplateId(bundle, templatesById, library);
    if (templateID !== "none") return `${library.id}:${templateID}`;
  }
  return "none";
}

function parseHomeFeaturedValue(value: string): { libraryId: number; templateId: string } | null {
  if (value === "none") return null;
  const [libraryID, templateID] = value.split(":");
  const parsedLibraryID = Number(libraryID);
  if (!Number.isInteger(parsedLibraryID) || parsedLibraryID <= 0 || !templateID) return null;
  return { libraryId: parsedLibraryID, templateId: templateID };
}

function buildFeaturedBundleRequest(
  homeFeatured: string,
  libraryFeatured: Record<number, string>,
): ApplyCollectionTemplateBundleFeaturedRequest | undefined {
  const request: ApplyCollectionTemplateBundleFeaturedRequest = {};
  const home = parseHomeFeaturedValue(homeFeatured);
  if (home) {
    request.home = {
      library_id: home.libraryId,
      template_id: home.templateId,
    };
  }

  const libraries = Object.fromEntries(
    Object.entries(libraryFeatured).filter(([, templateID]) => templateID !== "none"),
  );
  if (Object.keys(libraries).length > 0) {
    request.libraries = libraries;
  }

  if (!request.home && !request.libraries) return undefined;
  return request;
}

function BundleApplyResult({ result }: { result: ApplyCollectionTemplateBundleResponse }) {
  const actionLabel = result.dry_run ? "Would create" : "Created";
  const deleteActionLabel = result.dry_run ? "Would delete" : "Deleted";
  const created = result.created ?? [];
  const skipped = result.skipped ?? [];
  const failed = result.failed ?? [];
  const deleted = result.deleted ?? [];
  const deleteFailed = result.delete_failed ?? [];
  const deleteSkipped = result.delete_skipped ?? [];
  const syncQueued = result.sync_queued ?? [];
  const featured = result.featured ?? [];
  const featuredFailed = result.featured_failed ?? [];
  return (
    <div className="space-y-2 rounded-md border p-3 text-sm">
      <div className="font-medium">
        {result.delete_existing ? (
          <>
            {deleteActionLabel} {deleted.length}; delete skipped {deleteSkipped.length}; delete
            failed {deleteFailed.length};{" "}
          </>
        ) : null}
        {actionLabel} {created.length}; skipped {skipped.length}; failed {failed.length}
        {syncQueued.length > 0 ? `; sync queued ${syncQueued.length}` : ""}; featured{" "}
        {featured.length}; featured failed {featuredFailed.length}
      </div>
      {syncQueued.length > 0 ? (
        <div className="text-muted-foreground text-xs leading-snug">
          Initial syncs are running in the background. Collections are available now; items appear
          as each sync finishes.
        </div>
      ) : null}
      {deleteFailed.length > 0 ? (
        <div className="text-destructive space-y-1 text-xs">
          {deleteFailed.slice(0, 5).map((entry) => (
            <div key={`delete:${entry.library_id}:${entry.collection_id}`}>
              {entry.library_name} / {entry.collection_title ?? entry.collection_id}:{" "}
              {entry.reason ?? "failed"}
            </div>
          ))}
        </div>
      ) : null}
      {failed.length > 0 ? (
        <div className="text-destructive space-y-1 text-xs">
          {failed.slice(0, 5).map((entry) => (
            <div key={`${entry.library_id}:${entry.template_id}`}>
              {entry.library_name} / {entry.template_title}: {entry.reason ?? "failed"}
            </div>
          ))}
        </div>
      ) : null}
      {featuredFailed.length > 0 ? (
        <div className="text-destructive space-y-1 text-xs">
          {featuredFailed.slice(0, 5).map((entry) => (
            <div
              key={`featured:${entry.surface}:${entry.library_id ?? "home"}:${entry.template_id}`}
            >
              {entry.surface === "home" ? "Home" : entry.library_name} / {entry.template_title}:{" "}
              {entry.reason ?? "failed"}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function CategoryPill({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded-full px-3 py-1 text-xs font-medium transition-colors " +
        (active
          ? "bg-primary text-primary-foreground"
          : "bg-muted text-muted-foreground hover:bg-muted/70")
      }
    >
      {label}
    </button>
  );
}
