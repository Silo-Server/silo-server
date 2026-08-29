import { useState } from "react";
import { useSearchParams } from "react-router";
import { ChevronDown, ChevronRight, Play } from "lucide-react";
import type { AutoscanSettings } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  useAutoscanSettings,
  useTriggerAutoscan,
  useUpdateAutoscanSettings,
} from "@/hooks/queries/useAutoscan";
import ConnectionsPanel from "@/pages/admin/autoscan/ConnectionsPanel";
import ActivityPanel from "@/pages/admin/autoscan/ActivityPanel";
import SourcesPanel from "@/pages/admin/autoscan/SourcesPanel";
import { isLegacyAdvancedTab, normalizeTab } from "@/pages/autoscanSearchParams";

// ---------------------------------------------------------------------------
// Settings tab
// ---------------------------------------------------------------------------

function SettingsTab() {
  const settings = useAutoscanSettings();
  const updateSettings = useUpdateAutoscanSettings();

  // Local form state — initialised from server data; reflected immediately on
  // every mutation so the UI stays responsive without waiting for refetch.
  const [form, setForm] = useState<AutoscanSettings | null>(null);

  // Merge server data into local form on first load (and after invalidation).
  const serverData = settings.data;
  const effective: AutoscanSettings = form ??
    serverData ?? { enabled: false, default_poll_interval_seconds: 300, debounce_seconds: 10 };

  function patch(delta: Partial<AutoscanSettings>) {
    setForm((prev) => ({
      ...(prev ?? effective),
      ...delta,
    }));
  }

  function save(override?: Partial<AutoscanSettings>) {
    const body: AutoscanSettings = { ...effective, ...override };
    updateSettings.mutate(body, {
      onSuccess: () => setForm(null), // reset to server truth after save
    });
  }

  if (settings.isLoading) {
    return <p className="text-muted-foreground py-4 text-sm">Loading settings…</p>;
  }

  return (
    <div className="max-w-lg space-y-6">
      {/* Default poll interval */}
      <div className="space-y-1.5">
        <Label htmlFor="default-poll-interval">Default check interval (seconds)</Label>
        <div className="flex items-center gap-2">
          <Input
            id="default-poll-interval"
            className="w-32"
            type="number"
            min={1}
            value={effective.default_poll_interval_seconds}
            onChange={(e) =>
              patch({ default_poll_interval_seconds: Number(e.target.value) || 300 })
            }
            onBlur={() => save()}
          />
          <span className="text-muted-foreground text-sm">sec</span>
        </div>
        <p className="text-muted-foreground text-xs">
          Used by sources that don&apos;t set their own.
        </p>
      </div>

      {/* Debounce */}
      <div className="space-y-1.5">
        <Label htmlFor="debounce-seconds">Debounce (seconds)</Label>
        <div className="flex items-center gap-2">
          <Input
            id="debounce-seconds"
            className="w-32"
            type="number"
            min={0}
            value={effective.debounce_seconds}
            onChange={(e) => patch({ debounce_seconds: Number(e.target.value) || 0 })}
            onBlur={() => save()}
          />
          <span className="text-muted-foreground text-sm">sec</span>
        </div>
        <p className="text-muted-foreground text-xs">
          Coalesces rapid change events before triggering a scan.
        </p>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

interface AdminAutoscanProps {
  /**
   * Rendered inside the Libraries page rather than as its own route. The
   * heading drops to an h2 and the Sources/Activity selection moves to `view`,
   * because `tab` already names the Libraries tab that hosts this panel.
   */
  embedded?: boolean;
}

export default function AdminAutoscan({ embedded = false }: AdminAutoscanProps = {}) {
  const [searchParams, setSearchParams] = useSearchParams();
  const tabParam = embedded ? "view" : "tab";
  const requestedTab = searchParams.get(tabParam);
  const activeTab = normalizeTab(requestedTab);
  const trigger = useTriggerAutoscan();
  const settings = useAutoscanSettings();
  const updateSettings = useUpdateAutoscanSettings();

  // Open Advanced automatically when arriving from an old connections/settings
  // link, so a bookmark still lands on the thing it pointed at.
  const [advancedOpen, setAdvancedOpen] = useState(() => isLegacyAdvancedTab(requestedTab));

  const enabled = settings.data?.enabled ?? false;

  function toggleEnabled(checked: boolean) {
    if (!settings.data) return;
    updateSettings.mutate({ ...settings.data, enabled: checked });
  }

  function setActiveTab(value: string) {
    const next = new URLSearchParams(searchParams);
    if (value === "sources") {
      next.delete(tabParam);
    } else {
      next.set(tabParam, value);
    }
    setSearchParams(next, { replace: true });
  }

  return (
    <div className="space-y-6">
      <div className="page-header">
        <div className="space-y-2">
          <div className="flex items-center gap-2.5">
            {embedded ? (
              <h2 className="text-xl font-semibold tracking-tight">Autoscan</h2>
            ) : (
              <h1 className="text-3xl font-semibold tracking-normal text-balance sm:text-4xl">
                Autoscan
              </h1>
            )}
            {settings.data &&
              (enabled ? (
                <Badge variant="secondary">Enabled</Badge>
              ) : (
                <Badge variant="outline" className="text-muted-foreground">
                  Disabled
                </Badge>
              ))}
          </div>
          <p className="text-muted-foreground max-w-2xl text-sm leading-6">
            Silo re-scans a library as soon as something changes, instead of waiting for the next
            scheduled scan. Add a source for each thing you want watched.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <Label htmlFor="autoscan-enabled" className="text-muted-foreground text-sm">
              Autoscan
            </Label>
            <Switch
              id="autoscan-enabled"
              checked={enabled}
              onCheckedChange={toggleEnabled}
              disabled={!settings.data || updateSettings.isPending}
              aria-label="Enable autoscan"
            />
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={trigger.isPending}
            onClick={() => trigger.mutate()}
          >
            <Play />
            {trigger.isPending ? "Triggering…" : "Run now"}
          </Button>
        </div>
      </div>

      <Tabs value={activeTab} onValueChange={setActiveTab} className="gap-5">
        <TabsList variant="line" className="border-border w-full justify-start border-b">
          <TabsTrigger value="sources">Sources</TabsTrigger>
          <TabsTrigger value="activity">Activity</TabsTrigger>
        </TabsList>

        <TabsContent value="sources" className="space-y-6">
          <SourcesPanel />

          {/* Advanced — servers and polling defaults. Collapsed by default
              because most setups never need either: a webhook source needs no
              server, and the default interval is usually fine. */}
          <section className="border-border rounded-lg border">
            <button
              type="button"
              className="flex w-full items-center gap-2 px-4 py-3 text-left"
              onClick={() => setAdvancedOpen((open) => !open)}
              aria-expanded={advancedOpen}
              aria-controls="autoscan-advanced"
            >
              {advancedOpen ? (
                <ChevronDown className="text-muted-foreground size-4 shrink-0" />
              ) : (
                <ChevronRight className="text-muted-foreground size-4 shrink-0" />
              )}
              <span className="text-sm font-medium">Advanced</span>
              <span className="text-muted-foreground text-xs">
                Saved servers and polling defaults
              </span>
            </button>

            {advancedOpen && (
              <div id="autoscan-advanced" className="space-y-8 border-t px-4 py-5">
                <ConnectionsPanel />
                <div className="space-y-4">
                  <div className="space-y-1">
                    <h2 className="text-sm font-medium">Polling defaults</h2>
                    <p className="text-muted-foreground text-xs">
                      Applied to sources that don&apos;t override them.
                    </p>
                  </div>
                  <SettingsTab />
                </div>
              </div>
            )}
          </section>
        </TabsContent>

        <TabsContent value="activity">
          <ActivityPanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}
