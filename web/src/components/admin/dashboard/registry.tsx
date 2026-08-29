import type {
  DashboardLayoutEntry,
  DashboardWidgetDefinition,
  WidgetId,
  WidgetRangeOptions,
} from "./types";
import {
  ActiveStreamsStatWidget,
  EgressNowStatWidget,
  MoviesStatWidget,
  ProfilesActiveStatWidget,
  ShowsStatWidget,
  StorageStatWidget,
  TranscodeShareStatWidget,
  UsersStatWidget,
} from "./widgets/statTiles";
import { ConcurrentStreamsWidget } from "./widgets/ConcurrentStreamsWidget";
import { EgressWidget } from "./widgets/EgressWidget";
import { HealthStripWidget } from "./widgets/HealthStripWidget";
import { ServerResourcesWidget } from "./widgets/ServerResourcesWidget";
import { PlaybackActivityWidget } from "./widgets/PlaybackActivityWidget";
import { PlaybackReliabilityWidget } from "./widgets/PlaybackReliabilityWidget";
import { TopProfilesWidget } from "./widgets/TopProfilesWidget";
import { TopTitlesWidget } from "./widgets/TopTitlesWidget";
import { WatchProvidersWidget } from "./widgets/WatchProvidersWidget";
import { NowPlayingWidget } from "./widgets/NowPlayingWidget";
import { TranscodeNodesWidget } from "./widgets/TranscodeNodesWidget";
import { ScannerWidget } from "./widgets/ScannerWidget";
import { LibrariesWidget } from "./widgets/LibrariesWidget";
import { UsersWidget } from "./widgets/UsersWidget";
import { DownloadsWidget } from "./widgets/DownloadsWidget";
import { ScanActivityWidget } from "./widgets/ScanActivityWidget";
import { RecentErrorsWidget } from "./widgets/RecentErrorsWidget";
import { RecentActivityWidget } from "./widgets/RecentActivityWidget";

/**
 * Sampled charts read `dashboard_metric_samples`, which the sampler keeps for a
 * month, so they offer the full spread down to a single hour.
 */
const SAMPLED_RANGES: WidgetRangeOptions = {
  allowed: ["hour", "day", "week", "month"],
  default: "day",
};

/**
 * Leaderboards start at a day: an hour of watch history ranks too little to be
 * worth a chart, and the endpoint's window is measured in days anyway.
 */
const LEADERBOARD_RANGES: WidgetRangeOptions = {
  allowed: ["day", "week", "month"],
  default: "week",
};

export const DASHBOARD_WIDGETS: DashboardWidgetDefinition[] = [
  {
    id: "stat-active-streams",
    title: "Active streams",
    description: "Live count of playback sessions",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: ActiveStreamsStatWidget,
  },
  {
    id: "stat-egress-now",
    title: "Egress now",
    description: "Egress the deployment is serving this minute",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: EgressNowStatWidget,
  },
  {
    id: "stat-transcode-share",
    title: "Transcode share",
    description: "Share of live streams being transcoded",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: TranscodeShareStatWidget,
  },
  {
    id: "stat-profiles-active",
    title: "Profiles · 24h",
    description: "Profiles that watched something in the last 24 hours",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: ProfilesActiveStatWidget,
  },
  {
    id: "stat-movies",
    title: "Movies",
    description: "Total movies and movie files",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: MoviesStatWidget,
  },
  {
    id: "stat-shows",
    title: "Shows",
    description: "Total series and episode files",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: ShowsStatWidget,
  },
  {
    id: "stat-users",
    title: "User count",
    description: "Registered accounts on the server",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: UsersStatWidget,
  },
  {
    id: "stat-storage",
    title: "Storage",
    description: "Used space across all libraries",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: StorageStatWidget,
  },
  {
    id: "health-strip",
    title: "Server health",
    description: "Version, uptime, dependencies, nodes, and 24h error count",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: HealthStripWidget,
  },
  {
    id: "server-resources",
    title: "Server resources",
    description: "CPU, memory, disk, network, and GPU usage on the API host",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 4,
    defaultRows: 3,
    Component: ServerResourcesWidget,
  },
  {
    id: "playback-24h",
    title: "Playback activity",
    description: "Playback starts stacked by play method",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: SAMPLED_RANGES,
    Component: PlaybackActivityWidget,
  },
  {
    id: "concurrent-streams-24h",
    title: "Concurrent streams",
    description: "Sampled concurrent playback sessions",
    minSpan: 4,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: SAMPLED_RANGES,
    Component: ConcurrentStreamsWidget,
  },
  {
    id: "egress-24h",
    title: "Egress",
    description: "Sampled egress in Mbps",
    minSpan: 4,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: SAMPLED_RANGES,
    Component: EgressWidget,
  },
  {
    id: "playback-reliability",
    title: "Playback reliability",
    description: "Sessions started, transcode starts, completion rate, profiles",
    minSpan: 4,
    maxSpan: 8,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 4,
    defaultRows: 3,
    ranges: SAMPLED_RANGES,
    Component: PlaybackReliabilityWidget,
  },
  {
    id: "top-titles",
    title: "Top titles",
    description: "Most-played titles over the chosen window",
    minSpan: 4,
    maxSpan: 8,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: LEADERBOARD_RANGES,
    Component: TopTitlesWidget,
  },
  {
    id: "top-profiles",
    title: "Most active profiles",
    description: "Profiles with the most plays over the chosen window",
    minSpan: 4,
    maxSpan: 8,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: LEADERBOARD_RANGES,
    Component: TopProfilesWidget,
  },
  {
    id: "watch-providers",
    title: "Watch providers",
    description: "Per-provider connection and 24h sync status",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    // One compact row per provider. It starts at the single row it occupied as
    // a Trakt-only strip and grows to three for a deployment running several
    // providers; past that the list scrolls like the other list widgets.
    minRows: 1,
    maxRows: 3,
    defaultRows: 1,
    Component: WatchProvidersWidget,
  },
  {
    id: "now-playing",
    title: "Now playing",
    description: "Active streams with client, method, and progress",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    minRows: 2,
    maxRows: 8,
    defaultRows: 3,
    // An idle server has nothing to put in three rows, so the widget hands them
    // back and shows a one-line strip until a stream starts.
    collapsedRows: 1,
    Component: NowPlayingWidget,
  },
  {
    id: "transcode-nodes",
    title: "Nodes",
    description: "Stream node health, job load, and egress",
    minSpan: 4,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    Component: TranscodeNodesWidget,
  },
  {
    id: "scanner",
    title: "Scanner",
    description: "Live scan progress, queue depth, and autoscan state",
    minSpan: 4,
    maxSpan: 12,
    defaultSpan: 4,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    Component: ScannerWidget,
  },
  {
    id: "libraries",
    title: "Libraries",
    description: "Library list with scan controls and progress",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 7,
    minRows: 2,
    maxRows: 8,
    defaultRows: 4,
    Component: LibrariesWidget,
  },
  {
    id: "users",
    title: "Users",
    description: "Recent user accounts with role and status",
    minSpan: 4,
    maxSpan: 8,
    defaultSpan: 5,
    minRows: 2,
    maxRows: 8,
    defaultRows: 4,
    Component: UsersWidget,
  },
  {
    id: "downloads",
    title: "Offline downloads",
    description: "Who has media downloaded to devices, and how much",
    minSpan: 3,
    maxSpan: 8,
    defaultSpan: 4,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    Component: DownloadsWidget,
  },
  {
    id: "scan-activity",
    title: "Scan activity",
    description: "Recent scan runs with trigger, status, and duration",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 8,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    Component: ScanActivityWidget,
  },
  {
    id: "recent-errors",
    title: "Recent errors",
    description: "Latest error and warning lines from the operational log",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    Component: RecentErrorsWidget,
  },
  {
    id: "recent-activity",
    title: "Recent activity",
    description: "Feed of recently started playback sessions",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    minRows: 2,
    maxRows: 8,
    defaultRows: 4,
    Component: RecentActivityWidget,
  },
];

const WIDGETS_BY_ID = new Map(DASHBOARD_WIDGETS.map((widget) => [widget.id, widget]));

export function getDashboardWidget(id: WidgetId): DashboardWidgetDefinition {
  const widget = WIDGETS_BY_ID.get(id);
  if (!widget) {
    throw new Error(`Unknown dashboard widget: ${id}`);
  }
  return widget;
}

/**
 * Widget ids that were renamed after layouts had already been stored under
 * them. A saved arrangement is the admin's, not ours to discard, so an old id
 * resolves to the widget that replaced it instead of being dropped as unknown.
 * Sanitizing a layout dedupes by the resolved widget, so a document holding
 * both the old and the new id collapses to one entry.
 */
const LEGACY_WIDGET_IDS: Record<string, WidgetId> = {
  // Became the pluggable per-provider widget; the payload it reads is no
  // longer Trakt-specific.
  "trakt-sync": "watch-providers",
};

export function findDashboardWidget(id: string): DashboardWidgetDefinition | undefined {
  return WIDGETS_BY_ID.get((LEGACY_WIDGET_IDS[id] ?? id) as WidgetId);
}

/**
 * The order of the default arrangement: live numbers first, then the charts
 * that explain them, then the operational surfaces that are only interesting
 * when something is wrong. Admins who already customized their dashboard keep
 * their stored layout — this list is only the starting point, and every widget
 * stays available from the Add-widget sheet.
 *
 * Only the order lives here. Sizes and windows come from each widget's own
 * definition below, so a widget can never start out at a size its definition
 * disagrees with.
 */
const DEFAULT_LAYOUT_ORDER: WidgetId[] = [
  // Live numbers: one dense row of tiles, then the whole-deployment health
  // strip. Everything the default leaves out stays one click away in the
  // Add-widget sheet — a sane starting page beats an exhaustive one.
  "stat-active-streams",
  "stat-egress-now",
  "stat-profiles-active",
  "stat-movies",
  "stat-shows",
  "stat-storage",
  "health-strip",
  // What is playing right now — the reason most admins open this page.
  "now-playing",
  // The trends that explain the numbers, then who watched what.
  "playback-24h",
  "egress-24h",
  "top-titles",
  "top-profiles",
  // Content management, then the history feed.
  "libraries",
  "users",
  "recent-activity",
];

/**
 * The default arrangement: every widget at its own default size, and each
 * ranged one on its own default window.
 */
export const DEFAULT_LAYOUT: DashboardLayoutEntry[] = DEFAULT_LAYOUT_ORDER.map((id) => {
  const widget = getDashboardWidget(id);
  const entry: DashboardLayoutEntry = {
    id: widget.id,
    span: widget.defaultSpan,
    rows: widget.defaultRows,
  };
  return widget.ranges ? { ...entry, range: widget.ranges.default } : entry;
});
