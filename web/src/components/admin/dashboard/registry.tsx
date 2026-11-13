import {
  defineDashboardWidget,
  type DashboardLayoutEntry,
  type DashboardWidgetDefinition,
  type WidgetId,
  type WidgetRangeOptions,
} from "./types";
import {
  ActiveStreamsStatWidget,
  AdvisoryCoverageStatWidget,
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
import { ApiClientsWidget } from "./widgets/ApiClientsWidget";

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
  defineDashboardWidget({
    id: "stat-active-streams",
    titleKey: "web.admin.dashboard.widgetCatalog.statActiveStreams.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statActiveStreams.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: ActiveStreamsStatWidget,
  }),
  defineDashboardWidget({
    id: "stat-egress-now",
    titleKey: "web.admin.dashboard.widgetCatalog.statEgressNow.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statEgressNow.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: EgressNowStatWidget,
  }),
  defineDashboardWidget({
    id: "stat-transcode-share",
    titleKey: "web.admin.dashboard.widgetCatalog.statTranscodeShare.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statTranscodeShare.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: TranscodeShareStatWidget,
  }),
  defineDashboardWidget({
    id: "stat-profiles-active",
    titleKey: "web.admin.dashboard.widgetCatalog.statProfilesActive.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statProfilesActive.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: ProfilesActiveStatWidget,
  }),
  defineDashboardWidget({
    id: "stat-movies",
    titleKey: "web.admin.dashboard.widgetCatalog.statMovies.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statMovies.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: MoviesStatWidget,
  }),
  defineDashboardWidget({
    id: "stat-shows",
    titleKey: "web.admin.dashboard.widgetCatalog.statShows.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statShows.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: ShowsStatWidget,
  }),
  defineDashboardWidget({
    id: "stat-advisory-coverage",
    titleKey: "web.admin.dashboard.widgetCatalog.statAdvisoryCoverage.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statAdvisoryCoverage.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: AdvisoryCoverageStatWidget,
  }),
  defineDashboardWidget({
    id: "stat-users",
    titleKey: "web.admin.dashboard.widgetCatalog.statUsers.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statUsers.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: UsersStatWidget,
  }),
  defineDashboardWidget({
    id: "stat-storage",
    titleKey: "web.admin.dashboard.widgetCatalog.statStorage.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.statStorage.description",
    minSpan: 2,
    maxSpan: 4,
    defaultSpan: 2,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: StorageStatWidget,
  }),
  defineDashboardWidget({
    id: "health-strip",
    titleKey: "web.admin.dashboard.widgetCatalog.healthStrip.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.healthStrip.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    minRows: 1,
    maxRows: 2,
    defaultRows: 1,
    Component: HealthStripWidget,
  }),
  defineDashboardWidget({
    id: "server-resources",
    titleKey: "web.admin.dashboard.widgetCatalog.serverResources.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.serverResources.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 4,
    defaultRows: 2,
    Component: ServerResourcesWidget,
  }),
  defineDashboardWidget({
    id: "playback-24h",
    titleKey: "web.admin.dashboard.widgetCatalog.playback24h.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.playback24h.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: SAMPLED_RANGES,
    Component: PlaybackActivityWidget,
  }),
  defineDashboardWidget({
    id: "concurrent-streams-24h",
    titleKey: "web.admin.dashboard.widgetCatalog.concurrentStreams24h.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.concurrentStreams24h.description",
    minSpan: 4,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: SAMPLED_RANGES,
    Component: ConcurrentStreamsWidget,
  }),
  defineDashboardWidget({
    id: "egress-24h",
    titleKey: "web.admin.dashboard.widgetCatalog.egress24h.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.egress24h.description",
    minSpan: 4,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: SAMPLED_RANGES,
    Component: EgressWidget,
  }),
  defineDashboardWidget({
    id: "playback-reliability",
    titleKey: "web.admin.dashboard.widgetCatalog.playbackReliability.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.playbackReliability.description",
    minSpan: 4,
    maxSpan: 8,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 4,
    defaultRows: 3,
    ranges: SAMPLED_RANGES,
    Component: PlaybackReliabilityWidget,
  }),
  defineDashboardWidget({
    id: "top-titles",
    titleKey: "web.admin.dashboard.widgetCatalog.topTitles.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.topTitles.description",
    minSpan: 4,
    maxSpan: 8,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: LEADERBOARD_RANGES,
    Component: TopTitlesWidget,
  }),
  defineDashboardWidget({
    id: "top-profiles",
    titleKey: "web.admin.dashboard.widgetCatalog.topProfiles.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.topProfiles.description",
    minSpan: 4,
    maxSpan: 8,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    ranges: LEADERBOARD_RANGES,
    Component: TopProfilesWidget,
  }),
  defineDashboardWidget({
    id: "watch-providers",
    titleKey: "web.admin.dashboard.widgetCatalog.watchProviders.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.watchProviders.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    // One compact row per provider. It starts at a single row and grows to
    // three for a deployment running several providers; past that the list
    // scrolls like the other list widgets.
    minRows: 1,
    maxRows: 3,
    defaultRows: 1,
    Component: WatchProvidersWidget,
  }),
  defineDashboardWidget({
    id: "now-playing",
    titleKey: "web.admin.dashboard.widgetCatalog.nowPlaying.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.nowPlaying.description",
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
  }),
  defineDashboardWidget({
    id: "transcode-nodes",
    titleKey: "web.admin.dashboard.widgetCatalog.transcodeNodes.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.transcodeNodes.description",
    minSpan: 4,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    Component: TranscodeNodesWidget,
  }),
  defineDashboardWidget({
    id: "scanner",
    titleKey: "web.admin.dashboard.widgetCatalog.scanner.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.scanner.description",
    minSpan: 4,
    maxSpan: 12,
    defaultSpan: 4,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    Component: ScannerWidget,
  }),
  defineDashboardWidget({
    id: "libraries",
    titleKey: "web.admin.dashboard.widgetCatalog.libraries.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.libraries.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 7,
    minRows: 2,
    maxRows: 8,
    defaultRows: 4,
    Component: LibrariesWidget,
  }),
  defineDashboardWidget({
    id: "users",
    titleKey: "web.admin.dashboard.widgetCatalog.users.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.users.description",
    minSpan: 4,
    maxSpan: 8,
    defaultSpan: 5,
    minRows: 2,
    maxRows: 8,
    defaultRows: 4,
    Component: UsersWidget,
  }),
  defineDashboardWidget({
    id: "downloads",
    titleKey: "web.admin.dashboard.widgetCatalog.downloads.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.downloads.description",
    minSpan: 3,
    maxSpan: 8,
    defaultSpan: 4,
    minRows: 2,
    maxRows: 5,
    defaultRows: 3,
    Component: DownloadsWidget,
  }),
  defineDashboardWidget({
    id: "scan-activity",
    titleKey: "web.admin.dashboard.widgetCatalog.scanActivity.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.scanActivity.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 8,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    Component: ScanActivityWidget,
  }),
  defineDashboardWidget({
    id: "recent-errors",
    titleKey: "web.admin.dashboard.widgetCatalog.recentErrors.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.recentErrors.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    Component: RecentErrorsWidget,
  }),
  defineDashboardWidget({
    id: "recent-activity",
    titleKey: "web.admin.dashboard.widgetCatalog.recentActivity.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.recentActivity.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 12,
    minRows: 2,
    maxRows: 8,
    defaultRows: 4,
    Component: RecentActivityWidget,
  }),
  defineDashboardWidget({
    id: "api-clients",
    titleKey: "web.admin.dashboard.widgetCatalog.apiClients.title",
    descriptionKey: "web.admin.dashboard.widgetCatalog.apiClients.description",
    minSpan: 6,
    maxSpan: 12,
    defaultSpan: 6,
    minRows: 2,
    maxRows: 6,
    defaultRows: 3,
    // API usage is summarized per hour and kept for 30 days: the same spread
    // as the sampled charts.
    ranges: SAMPLED_RANGES,
    Component: ApiClientsWidget,
  }),
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
  // The old single-provider sync widget, replaced by the per-provider one.
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
