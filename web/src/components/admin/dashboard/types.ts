import type React from "react";

import { t, type MessageKey } from "@/i18n";

export type WidgetId =
  | "stat-active-streams"
  | "stat-egress-now"
  | "stat-transcode-share"
  | "stat-profiles-active"
  | "stat-movies"
  | "stat-shows"
  | "stat-advisory-coverage"
  | "stat-users"
  | "stat-storage"
  | "health-strip"
  | "server-resources"
  | "playback-24h"
  | "concurrent-streams-24h"
  | "egress-24h"
  | "playback-reliability"
  | "top-titles"
  | "top-profiles"
  | "watch-providers"
  | "now-playing"
  | "transcode-nodes"
  | "scanner"
  | "libraries"
  | "users"
  | "downloads"
  | "scan-activity"
  | "recent-errors"
  | "recent-activity"
  | "api-clients";

/**
 * The window a metric widget covers.
 *
 * Named by period rather than by number so one stored value can mean 1 hour on
 * a sampled chart and 1 day on a leaderboard — the endpoints take different
 * units, and the admin is choosing "how far back", not "how many hours".
 */
export type WidgetRange = "hour" | "day" | "week" | "month";

/** The ranges a widget offers, and the one it starts on. */
export interface WidgetRangeOptions {
  allowed: readonly WidgetRange[];
  default: WidgetRange;
}

/**
 * A widget's identity and the box it is allowed to occupy.
 *
 * Columns and rows are independent axes: `minSpan === maxSpan` pins the width,
 * `minRows === maxRows` pins the height, and a widget that pins both cannot be
 * resized at all. Rows are grid rows (`--admin-row-h` in app.css), not pixels,
 * and only apply from the lg breakpoint up.
 *
 * `ranges` is absent for widgets whose window is not the admin's to choose —
 * live tiles, lists that are not time-bounded, and the fixed-24h stats.
 */
export interface DashboardWidgetDefinition {
  id: WidgetId;
  /** Catalog key of the widget's name; the layout stores `id`, never this text. */
  titleKey: MessageKey;
  /** Catalog key of the one-line summary in the Add-widget sheet. */
  descriptionKey: MessageKey;
  /** `titleKey` in the current interface language, read when accessed. */
  readonly title: string;
  /** `descriptionKey` in the current interface language, read when accessed. */
  readonly description: string;
  minSpan: number;
  maxSpan: number;
  defaultSpan: number;
  minRows: number;
  maxRows: number;
  defaultRows: number;
  /**
   * Height to fall back to while the widget reports it has nothing to show.
   *
   * Opt-in: a widget with no `collapsedRows` always occupies the rows the admin
   * gave it. The one that has it renders a slim strip instead of a tall empty
   * box on an idle server, and springs back to `rows` the moment it has
   * content. Customize mode ignores it so drag and resize targets stay put.
   */
  collapsedRows?: number;
  ranges?: WidgetRangeOptions;
  Component: React.ComponentType;
}

/** A registry entry as written: the text fields are derived from the keys. */
export type DashboardWidgetSpec = Omit<DashboardWidgetDefinition, "title" | "description">;

/**
 * Adds `title` and `description` to a registry entry as getters, so the text
 * follows the interface language instead of being frozen when the module loads.
 */
export function defineDashboardWidget(spec: DashboardWidgetSpec): DashboardWidgetDefinition {
  return {
    ...spec,
    get title() {
      return t(spec.titleKey);
    },
    get description() {
      return t(spec.descriptionKey);
    },
  };
}

export interface DashboardLayoutEntry {
  id: WidgetId;
  span: number;
  rows: number;
  /** Absent for widgets without `ranges`; otherwise one of that widget's allowed values. */
  range?: WidgetRange;
}
