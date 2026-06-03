import { useState } from "react";
import { useSearchParams } from "react-router";
import { Play } from "lucide-react";
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
import SourcesPanel from "@/pages/admin/autoscan/SourcesPanel";

// ---------------------------------------------------------------------------
// Tab routing helpers
// ---------------------------------------------------------------------------

const AUTOSCAN_TABS = ["sources", "connections", "settings"] as const;
type AutoscanTab = (typeof AUTOSCAN_TABS)[number];

function normalizeTab(value: string | null): AutoscanTab {
  return AUTOSCAN_TABS.includes(value as AutoscanTab) ? (value as AutoscanTab) : "sources";
}

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
      <p className="text-muted-foreground text-sm">
        Autoscan can be enabled or disabled from the toggle in the page header. Tune polling
        behavior below.
      </p>

      {/* Default poll interval */}
      <div className="space-y-1.5">
        <Label htmlFor="default-poll-interval">Default poll interval (seconds)</Label>
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
          Used for sources that have no per-source interval set.
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

export default function AdminAutoscan() {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = normalizeTab(searchParams.get("tab"));
  const trigger = useTriggerAutoscan();
  const settings = useAutoscanSettings();
  const updateSettings = useUpdateAutoscanSettings();

  const enabled = settings.data?.enabled ?? false;

  function toggleEnabled(checked: boolean) {
    if (!settings.data) return;
    updateSettings.mutate({ ...settings.data, enabled: checked });
  }

  function setActiveTab(value: string) {
    const next = new URLSearchParams(searchParams);
    if (value === "sources") {
      next.delete("tab");
    } else {
      next.set("tab", value);
    }
    setSearchParams(next, { replace: true });
  }

  return (
    <div className="space-y-6">
      <div className="page-header">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="space-y-2">
            <div className="flex items-center gap-2.5">
              <h1 className="text-3xl font-semibold tracking-normal text-balance sm:text-4xl">
                Autoscan
              </h1>
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
              Automatically detect library changes via arr webhook sources. Configure connections,
              enable scan sources, and tune polling intervals.
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
      </div>

      <Tabs value={activeTab} onValueChange={setActiveTab} className="gap-5">
        <TabsList variant="line" className="border-border w-full justify-start border-b">
          <TabsTrigger value="sources">Sources</TabsTrigger>
          <TabsTrigger value="connections">Connections</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="sources">
          <SourcesPanel />
        </TabsContent>

        <TabsContent value="connections">
          <ConnectionsPanel />
        </TabsContent>

        <TabsContent value="settings">
          <SettingsTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}
