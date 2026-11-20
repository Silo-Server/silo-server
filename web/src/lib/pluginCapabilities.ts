import {
  AudioWaveform,
  BookOpen,
  CalendarClock,
  Headphones,
  Image,
  Inbox,
  KeyRound,
  Network,
  Puzzle,
  RefreshCw,
  ScanLine,
  SkipForward,
  Tag,
  type LucideIcon,
} from "lucide-react";

import type { PluginCapability } from "@/api/types";
import { englishText, t, type MessageKey } from "@/i18n";
import { appLanguage } from "@/lib/appLocale";

export interface CapabilityKind {
  /** Stable key for the catalog's job filter (`catalog_job`). */
  job: string;
  label: string;
  icon: LucideIcon;
  /**
   * Whether the capability is a job an admin would pick a plugin for. Plumbing
   * capabilities (event consumers, HTTP routes) get no icon or filter chip.
   */
  primary: boolean;
}

/** A known kind as stored: its label is a catalog key, formatted on each read. */
type KnownKind = Omit<CapabilityKind, "label"> & { labelKey: MessageKey };

// Capability types the plugin SDK knows (pluginsdk/capability KnownTypes), in
// the order their icons and filter chips appear.
const KINDS: [string, KnownKind][] = [
  [
    "metadata_provider.v1",
    {
      job: "metadata",
      labelKey: "web.admin.plugins.capabilities.metadata",
      icon: Tag,
      primary: true,
    },
  ],
  [
    "image_resolver.v1",
    {
      job: "artwork",
      labelKey: "web.admin.plugins.capabilities.artwork",
      icon: Image,
      primary: true,
    },
  ],
  [
    "marker_provider.v1",
    {
      job: "markers",
      labelKey: "web.admin.plugins.capabilities.markers",
      icon: SkipForward,
      primary: true,
    },
  ],
  [
    "scan_source.v1",
    {
      job: "scanning",
      labelKey: "web.admin.plugins.capabilities.scanning",
      icon: ScanLine,
      primary: true,
    },
  ],
  [
    "request_router.v1",
    {
      job: "requests",
      labelKey: "web.admin.plugins.capabilities.requests",
      icon: Inbox,
      primary: true,
    },
  ],
  [
    "watch_sync_provider.v1",
    {
      job: "watch-sync",
      labelKey: "web.admin.plugins.capabilities.watchSync",
      icon: RefreshCw,
      primary: true,
    },
  ],
  [
    "auth_provider.v1",
    {
      job: "sign-in",
      labelKey: "web.admin.plugins.capabilities.signIn",
      icon: KeyRound,
      primary: true,
    },
  ],
  [
    "scheduled_task.v1",
    {
      job: "tasks",
      labelKey: "web.admin.plugins.capabilities.scheduledTasks",
      icon: CalendarClock,
      primary: true,
    },
  ],
  [
    "media_analyzer.v1",
    {
      job: "analysis",
      labelKey: "web.admin.plugins.capabilities.mediaAnalysis",
      icon: AudioWaveform,
      primary: true,
    },
  ],
  [
    "audiobook_backend.v1",
    {
      job: "audiobooks",
      labelKey: "web.admin.plugins.capabilities.audiobooks",
      icon: Headphones,
      primary: true,
    },
  ],
  [
    "ebook_backend.v1",
    {
      job: "ebooks",
      labelKey: "web.admin.plugins.capabilities.ebooks",
      icon: BookOpen,
      primary: true,
    },
  ],
  [
    "network_access_provider.v1",
    {
      job: "network",
      labelKey: "web.admin.plugins.capabilities.networkAccess",
      icon: Network,
      primary: true,
    },
  ],
  [
    "event_consumer.v1",
    {
      job: "events",
      labelKey: "web.admin.plugins.capabilities.events",
      icon: Puzzle,
      primary: false,
    },
  ],
  [
    "http_routes.v1",
    {
      job: "pages",
      labelKey: "web.admin.plugins.capabilities.webPages",
      icon: Puzzle,
      primary: false,
    },
  ],
];

const KIND_BY_TYPE = new Map(KINDS);
const KIND_ORDER = new Map(KINDS.map(([type], index) => [type, index]));

export function capabilityKind(type: string): CapabilityKind {
  const known = KIND_BY_TYPE.get(type);
  if (known) {
    const { labelKey, ...kind } = known;
    return { ...kind, label: t(labelKey) };
  }
  // An unknown type shows its own prefix, humanized, not catalog text.
  const prefix = type.split(".")[0] ?? type;
  const label = prefix.replace(/_/g, " ").replace(/^\w/, (c) => c.toUpperCase()) || type;
  return { job: prefix, label, icon: Puzzle, primary: false };
}

/** A known type's label in English, for search beside the shown label; undefined otherwise. */
export function capabilityEnglishLabel(type: string): string | undefined {
  const known = KIND_BY_TYPE.get(type);
  return known ? englishText(known.labelKey) : undefined;
}

/** A plugin's primary capability kinds, deduplicated and in a fixed order. */
export function pluginCapabilityKinds(capabilities: PluginCapability[]): CapabilityKind[] {
  const types = [...new Set(capabilities.map((capability) => capability.type))]
    .filter((type) => capabilityKind(type).primary)
    .sort((a, b) => (KIND_ORDER.get(a) ?? 999) - (KIND_ORDER.get(b) ?? 999));
  return types.map(capabilityKind);
}

export function capabilityListLabel(capabilities: PluginCapability[]): string {
  return joinCommaList(pluginCapabilityKinds(capabilities).map((kind) => kind.label));
}

/** "a, b, c" in English; other interface languages use their own list separator. */
export function joinCommaList(items: string[]): string {
  return new Intl.ListFormat(appLanguage(), { type: "unit", style: "short" }).format(items);
}

/** The jobs present among `capabilitySets`, in display order, for the catalog filter. */
export function catalogJobs(capabilitySets: PluginCapability[][]): CapabilityKind[] {
  const seen = new Map<string, CapabilityKind>();
  for (const capabilities of capabilitySets) {
    for (const kind of pluginCapabilityKinds(capabilities)) seen.set(kind.job, kind);
  }
  const order = KINDS.map(([, kind]) => kind.job);
  return [...seen.values()].sort((a, b) => order.indexOf(a.job) - order.indexOf(b.job));
}

export function hasCapabilityJob(capabilities: PluginCapability[], job: string): boolean {
  return pluginCapabilityKinds(capabilities).some((kind) => kind.job === job);
}
