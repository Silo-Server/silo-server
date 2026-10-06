import { useState, type ReactNode } from "react";
import { AlertTriangle, Copy, Ellipsis, Pencil, RefreshCw, Trash2, Webhook } from "lucide-react";

import { captureProfileRequestContext } from "@/api/client";
import type { AutoscanScanSourceDescriptor, AutoscanSource, Library } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Switch } from "@/components/ui/switch";
import { useAutoscanSourceBusy, useUpdateAutoscanSource } from "@/hooks/queries/useAutoscan";
import { formatRelativeTime } from "@/lib/date";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

import { connectionIsMandatory } from "./sourceDescriptor";
import { sourceHealth, type SourceDisplay } from "./sourceDisplay";
import { isWebhookSource, sourceUpdateBody } from "./sourceForm";
import { sourceTargets } from "./sourceTargets";
import { useWebhookEndpoint } from "./useWebhookEndpoint";
import { RotateWebhookDialog } from "./WebhookEndpoint";

/**
 * The Sources list: one read-only row per source. Everything that changes a
 * source lives behind Edit; only the enabled switch, copying a webhook URL and
 * the menu actions stay on the row.
 *
 * Rows are a grid that lays out as columns once the list is wide enough and
 * stacks into cards below that. The breakpoint is the list's own width, not
 * the viewport's, because the admin sidebar takes a different share of it at
 * each size.
 */

// Fixed widths for the switch and actions columns keep rows aligned with the
// header even when a row has no Copy button.
const COLUMNS = "@2xl:grid-cols-[minmax(0,1.7fr)_minmax(0,0.9fr)_minmax(0,1.3fr)_3rem_9.25rem]";

export function SourceList({ children }: { children: ReactNode }) {
  return (
    <div className="@container">
      <div className="bg-card overflow-hidden rounded-lg border">
        <div
          className={cn(
            "text-muted-foreground hidden gap-x-4 border-b px-4 py-2.5 text-xs font-medium @2xl:grid",
            COLUMNS,
          )}
          aria-hidden
        >
          <span>Source</span>
          <span>Feeds</span>
          <span>Status</span>
          <span>On</span>
          <span />
        </div>
        <ul className="divide-border divide-y">{children}</ul>
      </div>
    </div>
  );
}

function relative(iso: string): string {
  return (
    formatRelativeTime(iso, { rounding: "floor", justNowLabel: "just now" }) ?? "at an unknown time"
  );
}

function StatusDot({ className }: { className: string }) {
  return <span aria-hidden className={cn("mt-2 size-1.5 shrink-0 rounded-full", className)} />;
}

/**
 * `hideErrorDetail` drops the plugin's error text when the row already says
 * what is wrong in its own words (a missing server).
 */
function SourceStatus({
  source,
  hideErrorDetail = false,
}: {
  source: AutoscanSource;
  hideErrorDetail?: boolean;
}) {
  const health = sourceHealth(source);
  switch (health.kind) {
    case "off":
      return (
        <p className="text-muted-foreground flex items-start gap-2 text-sm">
          <StatusDot className="bg-muted-foreground/60" />
          Off
        </p>
      );
    case "ok":
      return (
        <p
          className="text-success flex items-start gap-2 text-sm"
          title={formatDateTime(health.at)}
        >
          <StatusDot className="bg-success" />
          {health.headline} {relative(health.at)}
        </p>
      );
    case "idle":
      return (
        <p className="text-muted-foreground flex items-start gap-2 text-sm">
          <StatusDot className="bg-muted-foreground/60" />
          {health.headline}
        </p>
      );
    case "missing-endpoint":
      return (
        <p className="text-warning flex items-start gap-2 text-sm">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
          {health.headline}
        </p>
      );
    case "error":
      return (
        <div className="min-w-0 space-y-0.5">
          <p className="text-destructive flex flex-wrap items-center gap-x-1.5 text-sm font-medium">
            <AlertTriangle className="size-3.5 shrink-0" />
            {health.headline}
            {health.at && (
              <span className="text-destructive/80 font-normal">{relative(health.at)}</span>
            )}
          </p>
          {/* Clamped visually; the full text stays in the DOM for screen readers
              and in the title for pointer users. */}
          {!hideErrorDetail && (
            <p
              className="text-destructive/90 line-clamp-2 text-xs [overflow-wrap:anywhere] break-words"
              title={health.message}
            >
              {health.message}
            </p>
          )}
        </div>
      );
  }
}

function Warning({ tone, children }: { tone: "destructive" | "warning"; children: ReactNode }) {
  return (
    <p
      className={cn(
        "flex items-start gap-1.5 text-xs",
        tone === "destructive" ? "text-destructive" : "text-warning",
      )}
    >
      <AlertTriangle className="mt-px size-3.5 shrink-0" />
      <span className="min-w-0">{children}</span>
    </p>
  );
}

export function SourceListRow({
  source,
  display,
  descriptor,
  libraries,
  librariesLoading,
  onEdit,
  onRequestDelete,
}: {
  source: AutoscanSource;
  display: SourceDisplay;
  descriptor: AutoscanScanSourceDescriptor;
  libraries: readonly Library[];
  librariesLoading: boolean;
  onEdit: (source: AutoscanSource) => void;
  onRequestDelete: (source: AutoscanSource) => void;
}) {
  const [rowAuthority] = useState(captureProfileRequestContext);
  const update = useUpdateAutoscanSource(rowAuthority);
  // The switch and Edit resend this row's snapshot in full. While the list is
  // being re-read (e.g. right after an Edit save) or another write for this
  // source is in flight, that snapshot may be older than what is stored.
  const busy = useAutoscanSourceBusy(source.id);
  const endpoint = useWebhookEndpoint(source);
  const isWebhook = isWebhookSource(source);
  const { title } = display;

  function toggleEnabled(checked: boolean) {
    // The PUT replaces the source, so resend everything stored with it.
    update.mutate({ id: source.id, body: sourceUpdateBody(source, { enabled: checked }) });
  }

  // What this source keeps fresh. A source that can never resolve to a library
  // says so rather than running cleanly and silently doing nothing, which was
  // the most common "I set it up and nothing happened" report.
  const targets = sourceTargets(source, descriptor, libraries);
  const feeds = librariesLoading ? null : targets.unknown ? (
    <span className="text-muted-foreground text-xs">Determined at scan time</span>
  ) : targets.libraries.length === 0 ? (
    <span className="text-muted-foreground text-xs">None</span>
  ) : (
    targets.libraries.map((library) => (
      <Badge key={library.id} variant="secondary" className="max-w-full font-normal">
        <span className="truncate">{library.name}</span>
      </Badge>
    ))
  );

  const connectionMissing =
    !source.connection_id && connectionIsMandatory(descriptor, source.delivery_mode);

  const showCopy = isWebhook && endpoint.view.webhook_configured;
  const canRotate =
    isWebhook && endpoint.view.webhook_configured && endpoint.active && !endpoint.uncertain;

  return (
    <li
      className={cn(
        "grid items-center gap-x-4 gap-y-3 px-4 py-3.5",
        "grid-cols-[minmax(0,1fr)_auto] [grid-template-areas:'id_switch'_'feeds_feeds'_'status_status'_'actions_actions']",
        "@2xl:[grid-template-areas:'id_feeds_status_switch_actions']",
        COLUMNS,
      )}
    >
      <div className="flex min-w-0 items-center gap-3 [grid-area:id]">
        <span
          aria-hidden
          className="bg-muted text-muted-foreground grid size-9 shrink-0 place-items-center rounded-md"
        >
          {isWebhook ? <Webhook className="size-4" /> : <RefreshCw className="size-4" />}
        </span>
        <div className="min-w-0">
          <p className="line-clamp-2 text-sm font-medium break-words">{title}</p>
          <p className="text-muted-foreground text-xs break-words" title={source.plugin_id}>
            {display.subtitle}
          </p>
        </div>
      </div>

      <div className="flex min-w-0 flex-wrap items-center gap-1.5 [grid-area:feeds]">
        <span className="text-muted-foreground mr-1 text-xs @2xl:sr-only">Feeds</span>
        {feeds}
      </div>

      <div className="min-w-0 space-y-1 [grid-area:status]">
        <SourceStatus source={source} hideErrorDetail={connectionMissing} />
        {connectionMissing && (
          <Warning tone="destructive">No server selected. Edit the source to pick one.</Warning>
        )}
        {!librariesLoading && targets.unresolvable && (
          <Warning tone="destructive">
            No paths configured — this source can&apos;t match anything yet.
          </Warning>
        )}
        {!librariesLoading &&
          !targets.unresolvable &&
          !targets.unknown &&
          targets.libraries.length === 0 && (
            <Warning tone="warning">
              Paths don&apos;t match any library root — scans won&apos;t be enqueued.
            </Warning>
          )}
      </div>

      <div className="justify-self-end [grid-area:switch] @2xl:justify-self-start">
        <Switch
          checked={source.enabled}
          onCheckedChange={toggleEnabled}
          disabled={busy}
          aria-label={`${title} enabled`}
        />
      </div>

      <div className="flex items-center justify-end gap-1.5 [grid-area:actions]">
        {showCopy && (
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            disabled={!endpoint.url}
            onClick={() => endpoint.copy("open Edit and select the URL manually")}
            aria-label={`Copy webhook URL for ${title}`}
            title={endpoint.url ? "Copy webhook URL" : "Webhook URL unavailable — open Edit"}
          >
            <Copy />
          </Button>
        )}
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => onEdit(source)}
          // Edit starts its draft from this snapshot and saves it in full, so
          // it waits until a write or re-read for this source has landed.
          disabled={busy}
          aria-label={`Edit ${title}`}
        >
          <Pencil />
          Edit
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={`More actions for ${title}`}
            >
              <Ellipsis />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-48">
            {isWebhook && (
              <>
                <DropdownMenuItem disabled={!canRotate} onSelect={endpoint.requestRotate}>
                  <RefreshCw />
                  Rotate webhook URL
                </DropdownMenuItem>
                <DropdownMenuSeparator />
              </>
            )}
            <DropdownMenuItem variant="destructive" onSelect={() => onRequestDelete(source)}>
              <Trash2 />
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {isWebhook && <RotateWebhookDialog endpoint={endpoint} sourceName={title} />}
    </li>
  );
}
