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

// Capability types the plugin SDK knows (pluginsdk/capability KnownTypes), in
// the order their icons and filter chips appear.
const KINDS: [string, CapabilityKind][] = [
  ["metadata_provider.v1", { job: "metadata", label: "Metadata", icon: Tag, primary: true }],
  ["image_resolver.v1", { job: "artwork", label: "Artwork", icon: Image, primary: true }],
  ["marker_provider.v1", { job: "markers", label: "Markers", icon: SkipForward, primary: true }],
  ["scan_source.v1", { job: "scanning", label: "Scanning", icon: ScanLine, primary: true }],
  ["request_router.v1", { job: "requests", label: "Requests", icon: Inbox, primary: true }],
  [
    "watch_sync_provider.v1",
    { job: "watch-sync", label: "Watch sync", icon: RefreshCw, primary: true },
  ],
  ["auth_provider.v1", { job: "sign-in", label: "Sign-in", icon: KeyRound, primary: true }],
  [
    "scheduled_task.v1",
    { job: "tasks", label: "Scheduled tasks", icon: CalendarClock, primary: true },
  ],
  [
    "media_analyzer.v1",
    { job: "analysis", label: "Media analysis", icon: AudioWaveform, primary: true },
  ],
  [
    "audiobook_backend.v1",
    { job: "audiobooks", label: "Audiobooks", icon: Headphones, primary: true },
  ],
  ["ebook_backend.v1", { job: "ebooks", label: "Ebooks", icon: BookOpen, primary: true }],
  [
    "network_access_provider.v1",
    { job: "network", label: "Network access", icon: Network, primary: true },
  ],
  ["event_consumer.v1", { job: "events", label: "Events", icon: Puzzle, primary: false }],
  ["http_routes.v1", { job: "pages", label: "Web pages", icon: Puzzle, primary: false }],
];

const KIND_BY_TYPE = new Map(KINDS);
const KIND_ORDER = new Map(KINDS.map(([type], index) => [type, index]));

export function capabilityKind(type: string): CapabilityKind {
  const known = KIND_BY_TYPE.get(type);
  if (known) return known;
  const prefix = type.split(".")[0] ?? type;
  const label = prefix.replace(/_/g, " ").replace(/^\w/, (c) => c.toUpperCase()) || type;
  return { job: prefix, label, icon: Puzzle, primary: false };
}

/** A plugin's primary capability kinds, deduplicated and in a fixed order. */
export function pluginCapabilityKinds(capabilities: PluginCapability[]): CapabilityKind[] {
  const types = [...new Set(capabilities.map((capability) => capability.type))]
    .filter((type) => capabilityKind(type).primary)
    .sort((a, b) => (KIND_ORDER.get(a) ?? 999) - (KIND_ORDER.get(b) ?? 999));
  return types.map(capabilityKind);
}

export function capabilityListLabel(capabilities: PluginCapability[]): string {
  return pluginCapabilityKinds(capabilities)
    .map((kind) => kind.label)
    .join(", ");
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
