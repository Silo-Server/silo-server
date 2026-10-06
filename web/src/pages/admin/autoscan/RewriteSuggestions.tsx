import { useId, useLayoutEffect, useRef, useState } from "react";
import { ChevronDown, ChevronRight, RefreshCw } from "lucide-react";

import { isCapturedProfileAuthorityActive } from "@/api/client";
import type { AutoscanPathRewrite, AutoscanRewriteSuggestions } from "@/api/types";
import {
  captureAutoscanRewriteIntent,
  type AutoscanRewriteIntent,
} from "@/api/v2/adminAutoscanRewrites";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useAutoscanRewriteSuggestions } from "@/hooks/queries/useAutoscan";

/**
 * "Sync from server": reads the bound arr's root folders for a saved source and
 * offers rewrites for them. Nothing is written here — checked proposals are
 * handed to `onApply` and land in the dialog's draft, saved with everything
 * else.
 *
 * The read is tied to the source and the profile authority it was started
 * under; a result that arrives after either changed is dropped.
 */
export function RewriteSuggestions({
  sourceId,
  disabledReason,
  onApply,
}: {
  sourceId: string;
  /** Why syncing is unavailable right now; null when it can run. */
  disabledReason: string | null;
  onApply: (additions: AutoscanPathRewrite[]) => void;
}) {
  const suggest = useAutoscanRewriteSuggestions();
  const [previewState, setPreview] = useState<{
    value: AutoscanRewriteSuggestions;
    intent: AutoscanRewriteIntent;
  } | null>(null);
  const preview =
    previewState?.intent.sourceId === sourceId &&
    isCapturedProfileAuthorityActive(previewState.intent.profileContext)
      ? previewState.value
      : null;
  const requestScope = useRef({ sourceId, generation: 0 });
  useLayoutEffect(() => {
    const scope = requestScope.current;
    scope.sourceId = sourceId;
    scope.generation += 1;
    return () => {
      scope.generation += 1;
    };
  }, [sourceId]);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  async function handleSync() {
    try {
      const intent = captureAutoscanRewriteIntent(sourceId);
      const generation = ++requestScope.current.generation;
      const value = await suggest.mutateAsync(intent);
      if (
        requestScope.current.generation !== generation ||
        requestScope.current.sourceId !== intent.sourceId ||
        !isCapturedProfileAuthorityActive(intent.profileContext)
      )
        return;
      setPreview({ value, intent });
      setSelected(new Set(value.proposed.map((p) => p.from)));
    } catch {
      // The hook reports failures for the still-active authority. Keep the draft.
    }
  }

  function toggleSelected(from: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(from)) next.delete(from);
      else next.add(from);
      return next;
    });
  }

  function applySelected() {
    if (
      !preview ||
      !previewState ||
      !isCapturedProfileAuthorityActive(previewState.intent.profileContext)
    )
      return;
    onApply(
      preview.proposed.filter((p) => selected.has(p.from)).map((p) => ({ from: p.from, to: p.to })),
    );
    setPreview(null);
  }

  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={disabledReason !== null || suggest.isPending}
        onClick={handleSync}
        title={disabledReason ?? "Fetch root-folder mappings from the connected server"}
      >
        <RefreshCw className={suggest.isPending ? "animate-spin" : undefined} />
        {suggest.isPending ? "Syncing…" : "Sync from server"}
      </Button>
      {disabledReason && (
        <span className="text-muted-foreground basis-full text-xs">{disabledReason}</span>
      )}

      {preview && (
        <div
          className="border-border basis-full space-y-4 rounded-md border p-3"
          role="region"
          aria-label="Rewrite suggestions"
        >
          <div className="space-y-2">
            <p className="text-sm font-medium">Proposed</p>
            {preview.proposed.length === 0 ? (
              <p className="text-muted-foreground text-xs">No proposed rewrites.</p>
            ) : (
              preview.proposed.map((proposal) => (
                <label key={proposal.from} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={selected.has(proposal.from)}
                    onChange={() => toggleSelected(proposal.from)}
                  />
                  <span className="min-w-0 font-mono text-xs break-all">
                    {proposal.from} → {proposal.to}
                  </span>
                  {proposal.match_depth >= 2 ? (
                    <Badge variant="secondary" className="text-xs">
                      {`${proposal.match_depth} segments`}
                    </Badge>
                  ) : (
                    <Badge variant="destructive" className="text-xs">
                      1 segment — weak
                    </Badge>
                  )}
                </label>
              ))
            )}
          </div>

          {preview.unmatched.length > 0 && (
            <CollapsibleList
              title={`No Silo match (${preview.unmatched.length})`}
              items={preview.unmatched}
            />
          )}
          {preview.ambiguous.length > 0 && (
            <CollapsibleList
              title={`Ambiguous (${preview.ambiguous.length})`}
              items={preview.ambiguous.map((a) => `${a.root} → ${a.candidates.join(", ")}`)}
            />
          )}
          {preview.covered.length > 0 && (
            <CollapsibleList
              title={`Already mapped (${preview.covered.length})`}
              items={preview.covered}
            />
          )}

          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" size="sm" disabled={selected.size === 0} onClick={applySelected}>
              Add selected
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={() => setPreview(null)}>
              Dismiss
            </Button>
          </div>
        </div>
      )}
    </>
  );
}

/** Collapsed list section used inside the sync preview. */
function CollapsibleList({ title, items }: { title: string; items: string[] }) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  return (
    <div className="space-y-1">
      <button
        type="button"
        className="flex items-center gap-1.5 text-left"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-controls={panelId}
      >
        {open ? (
          <ChevronDown className="text-muted-foreground size-3.5 shrink-0" />
        ) : (
          <ChevronRight className="text-muted-foreground size-3.5 shrink-0" />
        )}
        <span className="text-sm font-medium">{title}</span>
      </button>
      {open && (
        <ul id={panelId} className="text-muted-foreground space-y-0.5 pl-5 text-xs">
          {items.map((item) => (
            <li key={item} className="font-mono break-all">
              {item}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
