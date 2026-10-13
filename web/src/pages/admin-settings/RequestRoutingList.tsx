import { useEffect, useMemo, useState, type ReactNode } from "react";
import { DndContext, type Announcements, type UniqueIdentifier } from "@dnd-kit/core";
import { SortableContext, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { AlertTriangle, GripVertical, MoreHorizontal, Plus } from "lucide-react";

import type { RequestIntegration } from "@/api/types";
import type { RequestRoute, RequestRouteDestination } from "@/api/v2/adminRequests";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  useReorderRequestRoutes,
  useRequestIntegrationOptions,
  useUpdateRequestRoute,
} from "@/hooks/queries/admin/requests";
import { useSortableList } from "@/hooks/useSortableList";
import { cn } from "@/lib/utils";

import { RequestFallbackEditor } from "./RequestFallbackEditor";
import { DeleteRuleDialog, RequestRuleEditor, type RoutingScope } from "./RequestRuleEditor";
import { AddRuleFlow } from "./RequestRulePresets";
import {
  destinationSentence,
  fallbackBody,
  passThroughLabel,
  routeBody,
  ruleSentence,
  sameDestination,
  SERVER_NAMED_OVERRIDES,
  serverOverrideFields,
} from "./requestRoutingModel";
import { serverFitsTier } from "./requestServerModel";
import {
  animeSeriesTypeTiers,
  fixLabel,
  routingWarnings,
  type RoutingWarning,
  type RoutingWarningFix,
  type RoutingWarningInput,
} from "./requestRoutingWarnings";

const KIND_NAMES = { movie: "Radarr", series: "Sonarr" } as const;

type DialogState =
  | { kind: "add" }
  | { kind: "custom" }
  | { kind: "edit"; id: string }
  | { kind: "fallback" }
  | null;

/**
 * A destination as one line, with quality profiles and tags named from the
 * server's options once they load (IDs until then).
 */
function DestinationText({ dest, scope }: { dest: RequestRouteDestination; scope: RoutingScope }) {
  const server = scope.allServers.find((candidate) => candidate.id === dest.integration_id);
  const needsNames = Object.keys(dest.overrides ?? {}).some((key) =>
    SERVER_NAMED_OVERRIDES.has(key),
  );
  const options = useRequestIntegrationOptions(needsNames && server ? server.id : undefined);
  const fields = server ? serverOverrideFields(server, scope.installations) : [];
  return <>{destinationSentence(dest, scope.allServers, { options: options.data, fields })}</>;
}

/** Amber warnings under a row, each with its fix when it has one. */
function RowWarnings({
  warnings,
  onFix,
  busy,
}: {
  warnings: readonly RoutingWarning[];
  onFix: (fix: RoutingWarningFix) => void;
  busy: boolean;
}) {
  if (warnings.length === 0) return null;
  return (
    <ul className="mt-1.5 flex list-none flex-col gap-1">
      {warnings.map((warning) => (
        <li
          key={warning.key}
          className="flex items-start gap-2 text-xs text-amber-600 dark:text-amber-400"
        >
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
            <span className="min-w-0 flex-[1_1_12rem]">{warning.text}</span>
            {warning.fix ? (
              <Button
                type="button"
                size="xs"
                variant="outline"
                className="ml-auto h-6 border-amber-500/40 text-amber-600 dark:text-amber-300"
                disabled={busy}
                onClick={() => onFix(warning.fix!)}
              >
                {fixLabel(warning.fix)}
              </Button>
            ) : null}
          </div>
        </li>
      ))}
    </ul>
  );
}

function RuleRow({
  rule,
  index,
  count,
  scope,
  warnings,
  highlighted,
  busy,
  onEdit,
  onToggle,
  onMove,
  onDelete,
  onFix,
}: {
  rule: RequestRoute;
  index: number;
  count: number;
  scope: RoutingScope;
  warnings: readonly RoutingWarning[];
  highlighted: boolean;
  busy: boolean;
  onEdit: () => void;
  onToggle: (enabled: boolean) => void;
  onMove: (delta: -1 | 1) => void;
  onDelete: () => void;
  onFix: (fix: RoutingWarningFix) => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: rule.id,
    disabled: busy,
  });
  const fallback = scope.fallback;
  const below = scope.rules.slice(index + 1);
  const fourK: ReactNode = rule.skip_uhd ? (
    fallback?.uhd.integration_id ? (
      "4K → none"
    ) : null
  ) : rule.uhd.integration_id && !sameDestination(rule.uhd, fallback?.uhd ?? {}) ? (
    <>
      4K → <DestinationText dest={rule.uhd} scope={scope} />
    </>
  ) : null;

  return (
    <li
      ref={setNodeRef}
      style={{
        transform: CSS.Transform.toString(transform),
        transition,
        opacity: isDragging ? 0.5 : 1,
      }}
      data-highlighted={highlighted || undefined}
      className={cn(
        "rounded-lg px-1 py-3 transition-colors duration-700",
        highlighted && "bg-[var(--settings-accent-soft)]",
      )}
    >
      <div className="flex items-start gap-2">
        <button
          type="button"
          aria-label={`Drag ${rule.name}`}
          className="text-muted-foreground hover:bg-accent mt-0.5 shrink-0 cursor-grab touch-none rounded-md p-1 disabled:cursor-not-allowed disabled:opacity-50"
          disabled={busy}
          {...attributes}
          {...listeners}
        >
          <GripVertical className="size-4" aria-hidden="true" />
        </button>
        <span
          className="text-muted-foreground mt-1 w-5 shrink-0 text-right text-xs tabular-nums"
          aria-hidden="true"
        >
          {index + 1}
        </span>
        <div className="min-w-0 flex-1">
          <button
            type="button"
            onClick={onEdit}
            className="focus-visible:ring-ring group block w-full rounded-sm text-left focus-visible:ring-2 focus-visible:outline-none"
          >
            <span
              className={cn(
                "flex flex-wrap items-center gap-2 text-sm font-medium group-hover:underline",
                !rule.enabled && "text-muted-foreground",
              )}
            >
              {rule.name}
              {!rule.enabled ? (
                <Badge variant="secondary" className="text-[10px]">
                  Off
                </Badge>
              ) : null}
            </span>
            <span
              className={cn(
                "text-muted-foreground block text-xs leading-relaxed",
                !rule.enabled && "opacity-70",
              )}
            >
              <span className="text-foreground/80 block">
                {ruleSentence(rule.conditions, scope.mediaType, scope.names)}
              </span>
              <span className="block">
                {rule.hd.integration_id ? (
                  <>
                    → <DestinationText dest={rule.hd} scope={scope} />
                  </>
                ) : (
                  `HD → ${passThroughLabel("hd", below, fallback, scope.allServers)}`
                )}
              </span>
              {fourK ? <span className="block">{fourK}</span> : null}
            </span>
          </button>
        </div>
        <Switch
          className="mt-0.5 shrink-0"
          checked={rule.enabled}
          onCheckedChange={onToggle}
          disabled={busy}
          aria-label={`${rule.name} enabled`}
        />
        {/* Not modal: Edit and Delete open a dialog, and a modal menu closing
          under it can leave the page unclickable. */}
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              size="icon-sm"
              variant="ghost"
              className="-mt-1 shrink-0"
              aria-label={`More for ${rule.name}`}
            >
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={onEdit}>Edit</DropdownMenuItem>
            <DropdownMenuItem disabled={busy || index === 0} onSelect={() => onMove(-1)}>
              Move up
            </DropdownMenuItem>
            <DropdownMenuItem disabled={busy || index === count - 1} onSelect={() => onMove(1)}>
              Move down
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" disabled={busy} onSelect={onDelete}>
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {/* Under the rule's text, but across to the row's right edge, so a fix
          button lines up with the switches above it. */}
      <div className="pl-15">
        <RowWarnings warnings={warnings} onFix={onFix} busy={busy} />
      </div>
    </li>
  );
}

/** Everything else, pinned last: a sentence once saved, a server picker until then. */
function FallbackRow({
  scope,
  warnings,
  busy,
  onEdit,
  onFix,
}: {
  scope: RoutingScope;
  warnings: readonly RoutingWarning[];
  busy: boolean;
  onEdit: () => void;
  onFix: (fix: RoutingWarningFix) => void;
}) {
  const fallback = scope.fallback;
  const save = useUpdateRequestRoute();
  const noun = scope.mediaType === "series" ? "series" : "movie";
  if (!fallback) return null;
  const saved = Boolean(fallback.hd.integration_id);

  return (
    <div
      role="group"
      aria-label="Everything else"
      className="border-border/60 flex items-start gap-2 border-t px-1 py-3"
    >
      <span className="mt-0.5 w-[3.25rem] shrink-0" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        {saved ? (
          <button
            type="button"
            onClick={onEdit}
            className="focus-visible:ring-ring group block w-full rounded-sm text-left focus-visible:ring-2 focus-visible:outline-none"
          >
            <span className="block text-sm font-medium group-hover:underline">Everything else</span>
            <span className="text-muted-foreground block text-xs leading-relaxed">
              <span className="text-foreground/80 block">
                Every other {noun} → <DestinationText dest={fallback.hd} scope={scope} />
              </span>
              <span className="block">
                4K →{" "}
                {fallback.uhd.integration_id ? (
                  <DestinationText dest={fallback.uhd} scope={scope} />
                ) : (
                  "none"
                )}
              </span>
            </span>
          </button>
        ) : (
          <div className="space-y-2">
            <p className="text-sm font-medium">Everything else</p>
            <p className="text-muted-foreground text-xs">
              Choose where {scope.mediaType === "series" ? "series" : "movies"} go when no rule
              takes them.
            </p>
            <Select
              value=""
              disabled={save.isPending || busy}
              onValueChange={(id) =>
                save.mutate({
                  route: fallback,
                  body: fallbackBody(fallback, {
                    hd: { integration_id: id, overrides: {} },
                    uhd: { integration_id: "", overrides: {} },
                  }),
                })
              }
            >
              <SelectTrigger aria-label="Everything else server" className="w-full sm:w-64">
                <SelectValue placeholder="Choose a server" />
              </SelectTrigger>
              <SelectContent>
                {scope.servers
                  .filter((server) => serverFitsTier(server, "hd"))
                  .map((server) => (
                    <SelectItem key={server.id} value={server.id}>
                      {server.name}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          </div>
        )}
        <RowWarnings warnings={warnings} onFix={onFix} busy={busy} />
      </div>
    </div>
  );
}

/**
 * One media type's routing: its rules in order (drag, or the ⋯ menu, to
 * reorder), Everything else pinned last, and "Add a rule". Every change saves
 * right away.
 */
export function RequestRoutingList({
  scope,
  routesFetching,
}: {
  scope: RoutingScope;
  /** The list is being read again; its order and validators may be stale. */
  routesFetching: boolean;
}) {
  const { mediaType, rules, fallback } = scope;
  const [dialog, setDialog] = useState<DialogState>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [highlight, setHighlight] = useState<string | null>(null);
  const toggleRule = useUpdateRequestRoute();
  const reorder = useReorderRequestRoutes();

  // Until the reread lands, the order and validators on screen are the ones a
  // reorder or save just replaced, so nothing may be computed from them.
  const busy = reorder.isPending || toggleRule.isPending || routesFetching;
  const ids = useMemo(() => rules.map((rule) => rule.id), [rules]);
  const sortable = useSortableList(
    rules,
    (rule: RequestRoute) => rule.id,
    (next: string[]) => reorder.mutate({ mediaType, ids: next }),
  );

  // Screen readers hear rule names and positions, not IDs.
  const nameOf = (id: UniqueIdentifier) => rules.find((rule) => rule.id === id)?.name ?? "The rule";
  const positionOf = (id: UniqueIdentifier) => ids.indexOf(String(id)) + 1;
  const announcements: Announcements = {
    onDragStart: ({ active }) =>
      `Picked up ${nameOf(active.id)}, rule ${positionOf(active.id)} of ${ids.length}.`,
    onDragOver: ({ active, over }) =>
      over
        ? `${nameOf(active.id)} is at position ${positionOf(over.id)} of ${ids.length}.`
        : `${nameOf(active.id)} is no longer over the list.`,
    onDragEnd: ({ active, over }) =>
      over
        ? `${nameOf(active.id)} dropped at position ${positionOf(over.id)} of ${ids.length}.`
        : `${nameOf(active.id)} dropped.`,
    onDragCancel: ({ active }) => `Moving ${nameOf(active.id)} was cancelled.`,
  };

  useEffect(() => {
    if (!highlight) return;
    const timer = window.setTimeout(() => setHighlight(null), 2500);
    return () => window.clearTimeout(timer);
  }, [highlight]);

  const warningInput: RoutingWarningInput = {
    mediaType,
    rules,
    fallback,
    servers: scope.allServers,
    serverFields: (server: RequestIntegration) => serverOverrideFields(server, scope.installations),
    language: scope.language,
    forceDual: scope.forceDual,
  };
  const warnings = routingWarnings(warningInput);

  function move(index: number, target: number) {
    if (target < 0 || target >= ids.length || target === index) return;
    const next = [...ids];
    const [moved] = next.splice(index, 1);
    next.splice(target, 0, moved!);
    reorder.mutate({ mediaType, ids: next });
  }

  function fix(rule: RequestRoute | undefined, action: RoutingWarningFix) {
    if (action.kind === "edit-fallback") {
      setDialog({ kind: "fallback" });
      return;
    }
    if (!rule) return;
    if (action.kind === "move-above") {
      const index = ids.indexOf(rule.id);
      const target = ids.indexOf(action.targetId);
      if (index > target && target !== -1) move(index, target);
      return;
    }
    const tiers = animeSeriesTypeTiers(rule, warningInput);
    const body = routeBody(rule);
    for (const tier of tiers) {
      const dest = body[tier];
      body[tier] = { ...dest, overrides: { ...(dest.overrides ?? {}), series_type: "anime" } };
    }
    toggleRule.mutate({ route: rule, body });
  }

  function closeDialog(addedId?: string) {
    setDialog(null);
    if (addedId) setHighlight(addedId);
  }

  const deleting = deletingId ? rules.find((rule) => rule.id === deletingId) : undefined;
  const editing = dialog?.kind === "edit" ? rules.find((rule) => rule.id === dialog.id) : undefined;
  const fallbackReady = Boolean(fallback?.hd.integration_id);
  const kindName = KIND_NAMES[mediaType];
  const noun = mediaType === "series" ? "series" : "movie";
  const plural = mediaType === "series" ? "series" : "movies";
  const fallbackServer = scope.allServers.find(
    (server) => server.id === fallback?.hd.integration_id,
  );

  if (scope.servers.length === 0 && rules.length === 0) {
    return (
      <p className="text-muted-foreground py-3.5 text-sm">
        Add a {kindName} server above to send {noun} requests anywhere.
      </p>
    );
  }

  return (
    <div className="py-2">
      {rules.length === 0 && fallbackReady ? (
        <p className="text-muted-foreground py-2 text-sm">
          Every {noun} request goes to {fallbackServer?.name ?? kindName}. Add a rule only if some{" "}
          {plural} should go somewhere else, like anime or kids&apos; titles.
        </p>
      ) : null}

      {rules.length > 0 ? (
        <DndContext
          sensors={sortable.sensors}
          collisionDetection={sortable.collisionDetection}
          onDragEnd={sortable.handleDragEnd}
          accessibility={{ announcements }}
        >
          <SortableContext items={ids} strategy={verticalListSortingStrategy}>
            <ol
              aria-label={`${mediaType === "series" ? "Series" : "Movie"} rules`}
              className="divide-border/60 list-none divide-y"
            >
              {rules.map((rule, index) => (
                <RuleRow
                  key={rule.id}
                  rule={rule}
                  index={index}
                  count={rules.length}
                  scope={scope}
                  warnings={warnings.get(rule.id) ?? []}
                  highlighted={highlight === rule.id}
                  busy={busy}
                  onEdit={() => setDialog({ kind: "edit", id: rule.id })}
                  onToggle={(enabled) =>
                    toggleRule.mutate({ route: rule, body: { ...routeBody(rule), enabled } })
                  }
                  onMove={(delta) => move(index, index + delta)}
                  onDelete={() => setDeletingId(rule.id)}
                  onFix={(action) => fix(rule, action)}
                />
              ))}
            </ol>
          </SortableContext>
        </DndContext>
      ) : null}

      <FallbackRow
        scope={scope}
        warnings={fallback ? (warnings.get(fallback.id) ?? []) : []}
        busy={busy}
        onEdit={() => setDialog({ kind: "fallback" })}
        onFix={(action) => fix(undefined, action)}
      />

      <div className="flex flex-wrap items-center gap-3 pt-1 pb-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={!fallbackReady}
          onClick={() => setDialog({ kind: "add" })}
        >
          <Plus aria-hidden="true" />
          Add a rule
        </Button>
        {!fallbackReady ? (
          <p className="text-muted-foreground text-xs">Choose where everything else goes first.</p>
        ) : null}
      </div>

      <Dialog
        open={dialog !== null && (dialog.kind !== "edit" || Boolean(editing))}
        onOpenChange={(open) => {
          if (!open) setDialog(null);
        }}
      >
        <DialogContent className="sm:max-w-2xl [&>*]:min-w-0">
          {dialog?.kind === "add" ? (
            <AddRuleFlow
              scope={scope}
              onCustom={() => setDialog({ kind: "custom" })}
              onDone={closeDialog}
            />
          ) : dialog?.kind === "custom" ? (
            <RequestRuleEditor key="new" scope={scope} source={null} onDone={closeDialog} />
          ) : dialog?.kind === "edit" && editing ? (
            // Keyed by id alone: a refresh that brings a newer revision must
            // not throw away edits; the save's 412 reports it instead.
            <RequestRuleEditor
              key={editing.id}
              scope={scope}
              source={editing}
              onDone={closeDialog}
            />
          ) : dialog?.kind === "fallback" && fallback ? (
            <RequestFallbackEditor scope={scope} route={fallback} onDone={() => setDialog(null)} />
          ) : null}
        </DialogContent>
      </Dialog>

      {deleting ? (
        // Read from the list each render, so a refresh brings its newest
        // validator; a refused (412) delete closes and the list is read again.
        <DeleteRuleDialog
          rule={deleting}
          open
          onOpenChange={(open) => {
            if (!open) setDeletingId(null);
          }}
        />
      ) : null}
    </div>
  );
}
