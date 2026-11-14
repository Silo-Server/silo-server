import { useId, type ReactNode } from "react";

import type { DiscoverFilter } from "@/api/v2/adminRequests";
import type { RequestMediaType } from "@/api/types";
import { Segmented } from "@/components/collections/fields/Segmented";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useRequestWatchProviders } from "@/hooks/queries/admin/requestDiscover";
import { useT } from "@/i18n";
import { ROUTING_COUNTRIES, ROUTING_LANGUAGES } from "@/lib/requestRoutingOptions";
import { tmdbGenresFor } from "@/lib/tmdbGenres";
import { ValuePicker, type Choice } from "@/pages/admin-settings/RequestRouteFields";
import { IdAdder } from "@/pages/admin-settings/RequestRuleConditions";

import { RATING_CEILINGS, sortForMediaType, sortsFor } from "./discoverLayoutModel";

const ANY = "__any__";

/** Picked IDs as the server takes them; none is absent. */
const toIDs = (ids: string[]) => (ids.length ? ids.map(Number) : undefined);

/** A labelled control with an optional line of help under it, as the row form lays them out. */
export function Field({
  label,
  help,
  children,
}: {
  label: string;
  help?: ReactNode;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <div role="group" aria-labelledby={id} className="grid gap-2">
      <span id={id} className="text-[14px] font-semibold">
        {label}
      </span>
      {children}
      {help ? <p className="text-muted-foreground text-[13px]">{help}</p> : null}
    </div>
  );
}

export function OptionSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: readonly Choice[];
  onChange: (value: string) => void;
}) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger aria-label={label} className="h-10 w-full sm:w-64">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/** A number box; empty clears the value. */
function NumberInput({
  label,
  value,
  onChange,
  min,
  max,
  step,
}: {
  label: string;
  value: number | undefined;
  onChange: (value: number | undefined) => void;
  min?: number;
  max?: number;
  step?: number;
}) {
  return (
    <Input
      type="number"
      inputMode="decimal"
      aria-label={label}
      min={min}
      max={max}
      step={step}
      className="h-10 w-28"
      value={value ?? ""}
      onChange={(event) => {
        const text = event.target.value.trim();
        const n = Number(text);
        onChange(text === "" || Number.isNaN(n) ? undefined : n);
      }}
    />
  );
}

/** TMDB IDs as chips, added by number. */
function IdList({
  label,
  ids,
  onChange,
}: {
  label: string;
  ids: number[] | undefined;
  onChange: (ids: number[] | undefined) => void;
}) {
  const values = (ids ?? []).map(String);
  const set = (next: string[]) => onChange(toIDs(next));
  return (
    <div className="flex flex-wrap items-center gap-2">
      <ValuePicker
        addLabel={label}
        options={[]}
        selected={values}
        onChange={set}
        labelOf={(value) => `#${value}`}
        hideAdd
      />
      <IdAdder label={label} onAdd={(id) => !values.includes(id) && set([...values, id])} />
    </div>
  );
}

/**
 * A TMDB filter row's filters. "primary" is what the row is about (kind,
 * order, genres, keywords, studios, streaming services) and goes in the form;
 * "more" (language, countries, years, scores, runtime, rating) goes under
 * More options, as Home rows split their settings.
 */
export function DiscoverFilterFields({
  filter,
  onChange,
  slot,
}: {
  filter: DiscoverFilter;
  onChange: (filter: DiscoverFilter) => void;
  slot: "primary" | "more";
}) {
  const t = useT();
  const mediaType = filter.media_type as RequestMediaType;
  const set = (change: Partial<DiscoverFilter>) => onChange({ ...filter, ...change });
  const region = filter.watch_region ?? "US";
  const providers = useRequestWatchProviders(mediaType, region);
  const genres = tmdbGenresFor(mediaType).map((g) => ({ value: String(g.id), label: g.name }));

  if (slot === "more") {
    return (
      <>
        <div className="flex flex-wrap gap-x-8 gap-y-4">
          <Field label={t("web.admin.requests.filter.originalLanguage")}>
            <OptionSelect
              label={t("web.admin.requests.filter.originalLanguage")}
              value={filter.original_language || ANY}
              options={[
                { value: ANY, label: t("web.admin.requests.filter.anyLanguage") },
                ...ROUTING_LANGUAGES.map((l) => ({ value: l.code, label: l.name })),
              ]}
              onChange={(v) => set({ original_language: v === ANY ? undefined : v })}
            />
          </Field>
          <Field
            label={t("web.admin.requests.filter.ratingLimit")}
            help={t("web.admin.requests.filter.ratingLimitHelp")}
          >
            <OptionSelect
              label={t("web.admin.requests.filter.ratingLimit")}
              value={filter.certification_lte || ANY}
              options={[
                { value: ANY, label: t("web.admin.requests.filter.noRatingLimit") },
                ...RATING_CEILINGS[mediaType].map((r) => ({
                  value: r,
                  label: t("web.admin.requests.filter.upToRating", { rating: r }),
                })),
              ]}
              onChange={(v) => set({ certification_lte: v === ANY ? undefined : v })}
            />
          </Field>
        </div>
        <Field
          label={t("web.admin.requests.filter.originCountries")}
          help={t("web.admin.requests.filter.originCountriesHelp")}
        >
          <ValuePicker
            addLabel={t("web.admin.requests.filter.addCountry")}
            options={ROUTING_COUNTRIES.map((c) => ({ value: c.code, label: c.name }))}
            selected={filter.origin_countries ?? []}
            onChange={(codes) => set({ origin_countries: codes.length ? codes : undefined })}
          />
        </Field>
        <div className="flex flex-wrap gap-x-8 gap-y-4">
          <Field
            label={
              mediaType === "series"
                ? t("web.admin.requests.filter.firstAired")
                : t("web.admin.requests.filter.released")
            }
          >
            <div className="flex items-center gap-2 text-sm">
              <NumberInput
                label={t("web.admin.requests.filter.fromYear")}
                value={filter.year_from}
                onChange={(v) => set({ year_from: v })}
              />
              <span className="text-muted-foreground">
                {t("web.admin.requests.filter.rangeTo")}
              </span>
              <NumberInput
                label={t("web.admin.requests.filter.toYear")}
                value={filter.year_to}
                onChange={(v) => set({ year_to: v })}
              />
            </div>
          </Field>
          <Field label={t("web.admin.requests.filter.runtime")}>
            <div className="flex items-center gap-2 text-sm">
              <NumberInput
                label={t("web.admin.requests.filter.shortestRuntime")}
                value={filter.runtime_gte}
                min={0}
                onChange={(v) => set({ runtime_gte: v })}
              />
              <span className="text-muted-foreground">
                {t("web.admin.requests.filter.rangeTo")}
              </span>
              <NumberInput
                label={t("web.admin.requests.filter.longestRuntime")}
                value={filter.runtime_lte}
                min={0}
                onChange={(v) => set({ runtime_lte: v })}
              />
            </div>
          </Field>
          <Field label={t("web.admin.requests.filter.lowestScore")}>
            <NumberInput
              label={t("web.admin.requests.filter.lowestScore")}
              value={filter.vote_average_gte}
              min={0}
              max={10}
              step={0.5}
              onChange={(v) => set({ vote_average_gte: v })}
            />
          </Field>
          <Field label={t("web.admin.requests.filter.fewestVotes")}>
            <NumberInput
              label={t("web.admin.requests.filter.fewestVotes")}
              value={filter.vote_count_gte}
              min={0}
              onChange={(v) => set({ vote_count_gte: v })}
            />
          </Field>
        </div>
      </>
    );
  }

  return (
    <div className="grid gap-5">
      <div className="flex flex-wrap gap-x-8 gap-y-4">
        <Segmented<RequestMediaType>
          label={t("web.admin.requests.filter.show")}
          value={mediaType}
          options={[
            { value: "movie", label: t("web.admin.requests.filter.movies") },
            { value: "series", label: t("web.admin.requests.filter.series") },
          ]}
          onChange={(next) =>
            // Genre and network IDs differ between movies and series.
            set({
              media_type: next,
              sort_by: sortForMediaType(filter.sort_by, next),
              genre_ids: undefined,
              exclude_genre_ids: undefined,
              network_ids: undefined,
              certification_lte: undefined,
            })
          }
        />
        <Field
          label={t("web.admin.requests.filter.order")}
          help={
            filter.sort_by.startsWith("vote_average")
              ? t("web.admin.requests.filter.ratedOrderHelp")
              : undefined
          }
        >
          <OptionSelect
            label={t("web.admin.requests.filter.order")}
            value={filter.sort_by}
            options={sortsFor(mediaType)}
            onChange={(v) => set({ sort_by: v })}
          />
        </Field>
      </div>
      <Field
        label={t("web.admin.requests.filter.genres")}
        help={t("web.admin.requests.filter.genresHelp")}
      >
        <ValuePicker
          addLabel={t("web.admin.requests.filter.addGenre")}
          options={genres}
          selected={(filter.genre_ids ?? []).map(String)}
          onChange={(ids) => set({ genre_ids: toIDs(ids) })}
        />
      </Field>
      <Field label={t("web.admin.requests.filter.excludeGenres")}>
        <ValuePicker
          addLabel={t("web.admin.requests.filter.addGenre")}
          options={genres}
          selected={(filter.exclude_genre_ids ?? []).map(String)}
          onChange={(ids) => set({ exclude_genre_ids: toIDs(ids) })}
        />
      </Field>
      <Field
        label={t("web.admin.requests.filter.keywords")}
        help={t("web.admin.requests.filter.keywordsHelp")}
      >
        <IdList
          label={t("web.admin.requests.filter.keywordId")}
          ids={filter.keyword_ids}
          onChange={(ids) => set({ keyword_ids: ids })}
        />
      </Field>
      <Field
        label={t("web.admin.requests.filter.studios")}
        help={t("web.admin.requests.filter.studiosHelp")}
      >
        <IdList
          label={t("web.admin.requests.filter.companyId")}
          ids={filter.company_ids}
          onChange={(ids) => set({ company_ids: ids })}
        />
      </Field>
      {mediaType === "series" ? (
        <Field
          label={t("web.admin.requests.filter.networks")}
          help={t("web.admin.requests.filter.networksHelp")}
        >
          <IdList
            label={t("web.admin.requests.filter.networkId")}
            ids={filter.network_ids}
            onChange={(ids) => set({ network_ids: ids })}
          />
        </Field>
      ) : null}
      <Field
        label={t("web.admin.requests.filter.streamingOn")}
        help={t("web.admin.requests.filter.streamingOnHelp")}
      >
        <div className="flex flex-wrap items-center gap-2">
          <Input
            aria-label={t("web.admin.requests.filter.region")}
            className="h-9 w-16 uppercase"
            maxLength={2}
            value={region}
            onChange={(event) => set({ watch_region: event.target.value.toUpperCase() })}
          />
          <ValuePicker
            addLabel={t("web.admin.requests.filter.addService")}
            options={(providers.data ?? []).map((p) => ({ value: String(p.id), label: p.name }))}
            selected={(filter.watch_provider_ids ?? []).map(String)}
            onChange={(ids) => set({ watch_provider_ids: toIDs(ids), watch_region: region })}
            unavailableHint={
              providers.isError ? t("web.admin.requests.filter.servicesFailed") : undefined
            }
          />
        </div>
      </Field>
    </div>
  );
}
