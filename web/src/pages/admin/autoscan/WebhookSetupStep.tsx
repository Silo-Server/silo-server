import { useState } from "react";
import { Check, Copy, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import type { AutoscanWebhookProvider } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

import type { MappingDraft } from "./webhookSetup";
import { expandedRootsFor, newMapping, settingsPathFor, triggersFor } from "./webhookSetup";

/**
 * The copy-the-URL-into-your-arr half of webhook setup.
 *
 * This exists because the previous flow created a webhook source and then left
 * the operator to work out, unaided, that they had to generate a URL, find the
 * right screen in Sonarr, and tick a specific set of boxes. Every one of those
 * is now stated on screen, and the trigger list is derived from what the host
 * actually parses rather than from memory.
 */
export function WebhookInstructions({
  url,
  provider,
}: {
  url: string;
  provider: AutoscanWebhookProvider | "auto";
}) {
  const [copied, setCopied] = useState(false);
  const triggers = triggersFor(provider);

  async function copyURL() {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error("Couldn't copy — select the URL and copy it manually.");
    }
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="webhook-url">1. Copy this URL</Label>
        <div className="flex gap-2">
          <Input
            id="webhook-url"
            readOnly
            value={url}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
          <Button type="button" variant="outline" size="sm" onClick={copyURL}>
            {copied ? <Check className="text-success" /> : <Copy />}
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
      </div>

      <div className="space-y-1.5">
        <Label>2. In your download manager, go to</Label>
        <p className="border-border bg-muted/30 rounded-md border px-3 py-2 font-mono text-xs">
          {settingsPathFor(provider)}
        </p>
        <p className="text-muted-foreground text-xs">
          Paste the URL into <span className="font-medium">Webhook URL</span> and leave the method
          as <span className="font-medium">POST</span>. No username or password is needed.
        </p>
      </div>

      <div className="space-y-2">
        <Label>3. Tick these triggers</Label>
        <ul className="space-y-2">
          {triggers.map((trigger) => (
            <li key={trigger.label} className="flex items-start gap-2 text-sm">
              <span
                aria-hidden
                className="border-muted-foreground/50 mt-0.5 grid size-4 shrink-0 place-items-center rounded-[3px] border"
              >
                <Check className="size-3" />
              </span>
              <span className="min-w-0">
                <span className="font-medium">{trigger.label}</span>
                {!trigger.required && <span className="text-muted-foreground"> (optional)</span>}
                <span className="text-muted-foreground block text-xs">{trigger.reason}</span>
              </span>
            </li>
          ))}
        </ul>
        <p className="text-muted-foreground text-xs">
          Leave every other trigger unchecked — Silo ignores them.
        </p>
      </div>

      <p className="text-muted-foreground text-xs">
        Save the connection in your download manager. You can use its
        <span className="font-medium"> Test </span> button — Silo accepts test payloads and will
        show the delivery on this source.
      </p>
    </div>
  );
}

/**
 * Path mapping editor for webhook sources.
 *
 * A webhook source has no connection, so the host's /suggest endpoint (which
 * reads an arr's root folders over its API) cannot run. The `to` side is
 * therefore pre-filled from real Silo library paths and the operator supplies
 * what their arr reports. Without at least one row, deliveries arrive and
 * resolve to nothing.
 */
export function WebhookMappingEditor({
  mappings,
  onChange,
  libraryPaths = [],
}: {
  mappings: MappingDraft[];
  onChange: (next: MappingDraft[]) => void;
  /**
   * Every library path the seeded rows were derived from. Used to offer a
   * per-branch breakdown when a row was collapsed to a shared root but the
   * operator's arr exposes those branches under different roots.
   */
  libraryPaths?: readonly string[];
}) {
  function update(index: number, patch: Partial<MappingDraft>) {
    onChange(mappings.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  }

  /** Replace a collapsed row with one row per child directory. */
  function expand(index: number, children: string[]) {
    const row = mappings[index];
    if (!row) return;
    onChange([
      ...mappings.slice(0, index),
      ...children.map((to) => newMapping(to, row.from)),
      ...mappings.slice(index + 1),
    ]);
  }

  return (
    <div className="space-y-3">
      <div className="space-y-1">
        <Label>Match its paths to yours</Label>
        <p className="text-muted-foreground text-xs">
          Sonarr/Radarr report the path of the <em>imported library file</em> — their root folder,
          not the download client&apos;s working directory. If that root differs from the path Silo
          sees, map it here. Same path on both sides? Enter it twice.
        </p>
      </div>

      {mappings.length === 0 ? (
        <p className="text-muted-foreground text-xs">
          No libraries found to map. Add a library first, or add a row manually.
        </p>
      ) : (
        <div className="space-y-2">
          {mappings.map((row, index) => {
            const children = expandedRootsFor(row.to, libraryPaths);
            return (
              <div key={row.id} className="space-y-1">
                <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                  <div className="min-w-0 flex-1 space-y-1">
                    <Label htmlFor={`map-from-${index}`} className="text-muted-foreground text-xs">
                      Sonarr/Radarr root folder
                    </Label>
                    <Input
                      id={`map-from-${index}`}
                      placeholder="/tv"
                      className="font-mono text-xs"
                      value={row.from}
                      onChange={(e) => update(index, { from: e.target.value })}
                    />
                  </div>
                  <span className="text-muted-foreground hidden pb-2 text-xs sm:block">→</span>
                  <div className="min-w-0 flex-1 space-y-1">
                    <Label htmlFor={`map-to-${index}`} className="text-muted-foreground text-xs">
                      Path Silo uses
                    </Label>
                    <Input
                      id={`map-to-${index}`}
                      placeholder="/mnt/media/tv"
                      className="font-mono text-xs"
                      value={row.to}
                      onChange={(e) => update(index, { to: e.target.value })}
                    />
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Remove mapping ${index + 1}`}
                    className="sm:mb-1"
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

      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange([...mappings, newMapping()])}
      >
        <Plus />
        Add a mapping
      </Button>
    </div>
  );
}
