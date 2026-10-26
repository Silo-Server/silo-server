import type { ReactNode } from "react";
import { Plus, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

import { expandedRootsFor, newMapping, type MappingDraft } from "./webhookSetup";

const COPY = {
  webhook: {
    heading: "Match its paths to yours",
    description: (
      <>
        Sonarr/Radarr report the path of the <em>imported library file</em> — their root folder, not
        the download client&apos;s working directory. If that root differs from the path Silo sees,
        map it here. Same path on both sides? Enter it twice.
      </>
    ),
    fromLabel: "Sonarr/Radarr root folder",
    fromPlaceholder: "/tv",
    empty: "No path mappings yet. Add one so deliveries reach a library.",
    emptyNoLibraries: "No libraries found to map. Add a library first, or add a row manually.",
  },
  poll: {
    heading: "Match paths",
    description: (
      <>
        Only needed when this source reports paths that differ from the ones Silo sees, for example
        when Sonarr runs in another container. Leave empty when both sides match.
      </>
    ),
    fromLabel: "Path the source reports",
    fromPlaceholder: "/data/media",
    empty: "No path mappings. Paths are used exactly as the source reports them.",
    emptyNoLibraries: "No path mappings. Paths are used exactly as the source reports them.",
  },
} as const;

/**
 * Path rewrite rows for any source: the path the provider reports (`from`) and
 * the Silo path it corresponds to (`to`). A webhook source has no connection to
 * read root folders from, so its rows are seeded from library paths and the
 * operator supplies the provider side.
 */
export function PathMappingEditor({
  mappings,
  onChange,
  variant,
  libraryPaths = [],
  invalidIds,
  idPrefix,
  actions,
}: {
  mappings: MappingDraft[];
  onChange: (next: MappingDraft[]) => void;
  variant: "webhook" | "poll";
  /**
   * Every library path the seeded rows were derived from. Used to offer a
   * per-branch breakdown when a row was collapsed to a shared root but the
   * operator's provider exposes those branches under different roots. Only
   * offered for the webhook variant.
   */
  libraryPaths?: readonly string[];
  /** Rows to flag as unfinished. */
  invalidIds?: ReadonlySet<string>;
  idPrefix: string;
  /** Extra buttons beside "Add a mapping", e.g. Sync from server. */
  actions?: ReactNode;
}) {
  const copy = COPY[variant];

  function update(index: number, patch: Partial<MappingDraft>) {
    onChange(mappings.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  }

  /**
   * Replace a collapsed row with one row per child directory. The provider
   * side starts blank: each branch has its own root there, and copying the
   * parent's into every row would map all of them to the first one.
   */
  function expand(index: number, children: string[]) {
    const row = mappings[index];
    if (!row) return;
    onChange([
      ...mappings.slice(0, index),
      ...children.map((to) => newMapping(to)),
      ...mappings.slice(index + 1),
    ]);
  }

  return (
    <div className="space-y-3">
      <div className="space-y-1">
        <Label>{copy.heading}</Label>
        <p className="text-muted-foreground text-xs">{copy.description}</p>
      </div>

      {mappings.length === 0 ? (
        <p className="text-muted-foreground text-xs">
          {libraryPaths.length > 0 ? copy.empty : copy.emptyNoLibraries}
        </p>
      ) : (
        <div className="space-y-2">
          {mappings.map((row, index) => {
            // Splitting a seeded root per type is about how Sonarr and Radarr
            // name their root folders; other sources report paths as they are.
            const children = variant === "webhook" ? expandedRootsFor(row.to, libraryPaths) : [];
            const invalid = invalidIds?.has(row.id) ?? false;
            return (
              <div key={row.id} className="space-y-1">
                <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                  <div className="min-w-0 flex-1 space-y-1">
                    <Label
                      htmlFor={`${idPrefix}-from-${index}`}
                      className="text-muted-foreground text-xs"
                    >
                      {copy.fromLabel}
                    </Label>
                    <Input
                      id={`${idPrefix}-from-${index}`}
                      placeholder={copy.fromPlaceholder}
                      className="font-mono text-xs"
                      value={row.from}
                      aria-invalid={invalid && row.from.trim() === ""}
                      onChange={(e) => update(index, { from: e.target.value })}
                    />
                  </div>
                  <span className="text-muted-foreground hidden pb-2 text-xs sm:block" aria-hidden>
                    →
                  </span>
                  <div className="min-w-0 flex-1 space-y-1">
                    <Label
                      htmlFor={`${idPrefix}-to-${index}`}
                      className="text-muted-foreground text-xs"
                    >
                      Path Silo uses
                    </Label>
                    <Input
                      id={`${idPrefix}-to-${index}`}
                      placeholder="/mnt/media/tv"
                      className="font-mono text-xs"
                      value={row.to}
                      aria-invalid={invalid && row.to.trim() === ""}
                      onChange={(e) => update(index, { to: e.target.value })}
                    />
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Remove mapping ${index + 1}`}
                    className="self-end sm:mb-1"
                    onClick={() => onChange(mappings.filter((_, i) => i !== index))}
                  >
                    <Trash2 className="text-destructive" />
                  </Button>
                </div>
                {children.length > 0 && (
                  <button
                    type="button"
                    className="text-muted-foreground hover:text-foreground text-xs underline-offset-4 hover:underline"
                    onClick={() => expand(index, children)}
                  >
                    Does your download manager use a different folder per type? Split into{" "}
                    {children.length} rows
                  </button>
                )}
              </div>
            );
          })}
        </div>
      )}

      {invalidIds && invalidIds.size > 0 && (
        <p className="text-destructive text-xs">
          Each mapping needs both paths. Fill in the highlighted fields or remove the row.
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => onChange([...mappings, newMapping()])}
        >
          <Plus />
          Add a mapping
        </Button>
        {actions}
      </div>
    </div>
  );
}
