import { useId, type ReactNode } from "react";
import { Lock } from "lucide-react";

import { RadioCardItem, RadioGroup } from "@/components/ui/radio-group";
import { useT } from "@/i18n";
import { chartRulesNote } from "@/lib/collections/copy";
import {
  chartHasTimeWindow,
  chartMediaLabel,
  chartWindowLabel,
  chartLockReason,
  chartMediaTypes,
  defaultChart,
  normalizeChart,
  TMDB_CHARTS,
  type TMDBChart,
  type TMDBChartMediaType,
  type TMDBChartPreset,
  type TMDBChartTimeWindow,
} from "@/lib/collections/tmdbSources";

import { Segmented } from "./Segmented";

/**
 * A TMDB chart: chart cards, then Show and "Trending over" as TMDB allows
 * them. Synced lists and Requests > Discover Page rows both pick charts here.
 */
export function TMDBChartField({
  chart,
  onChart,
  note,
}: {
  chart: TMDBChart | null;
  onChart: (chart: TMDBChart) => void;
  /** Said after the line about which choices each chart has. */
  note?: ReactNode;
}) {
  const t = useT();
  const id = useId();
  const allowed = chart ? chartMediaTypes(chart.preset) : [];
  const lock = chart ? chartLockReason(chart.preset) : null;
  const shows: TMDBChartMediaType[] =
    chart?.preset === "trending" ? ["movie", "tv", "all"] : ["movie", "tv"];
  return (
    <div className="grid gap-4">
      <div className="grid gap-2">
        <span id={`${id}-chart`} className="text-[14px] font-semibold">
          {t("web.collections.fields.chart")}
        </span>
        <RadioGroup
          aria-labelledby={`${id}-chart`}
          value={chart?.preset ?? ""}
          onValueChange={(value) => {
            const preset = value as TMDBChartPreset;
            onChart(chart ? normalizeChart({ ...chart, preset }) : defaultChart(preset));
          }}
          className="grid-cols-2 gap-2.5 sm:grid-cols-4"
        >
          {TMDB_CHARTS.map((entry) => (
            <RadioCardItem
              key={entry.preset}
              value={entry.preset}
              label={t(entry.labelKey)}
              hint={t(entry.hintKey)}
            />
          ))}
        </RadioGroup>
      </div>
      {chart ? (
        <div className="grid gap-2">
          <div className="flex flex-wrap gap-x-8 gap-y-3">
            <Segmented
              label={t("web.collections.fields.show")}
              value={chart.mediaType}
              options={shows.map((value) => ({
                value,
                label: chartMediaLabel(value),
                disabled: !allowed.includes(value),
              }))}
              onChange={(mediaType) => onChart({ ...chart, mediaType })}
            />
            {chartHasTimeWindow(chart.preset) ? (
              <Segmented<TMDBChartTimeWindow>
                label={t("web.collections.fields.trendingOver")}
                value={chart.timeWindow ?? "day"}
                options={(["day", "week"] as const).map((value) => ({
                  value,
                  label: chartWindowLabel(value),
                }))}
                onChange={(timeWindow) => onChart({ ...chart, timeWindow })}
              />
            ) : null}
          </div>
          {lock ? (
            <p className="text-muted-foreground flex items-center gap-1.5 text-[13px]">
              <Lock aria-hidden className="size-3.5" />
              {lock}
            </p>
          ) : null}
        </div>
      ) : null}
      <p className="text-muted-foreground text-[13px]">
        {chartRulesNote()}
        {note ? <> {note}</> : null}
      </p>
    </div>
  );
}
