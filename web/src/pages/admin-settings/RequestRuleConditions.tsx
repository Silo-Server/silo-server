import { Fragment, useId, useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";

import type { DiscoverBrandCard } from "@/api/types";
import type { RequestRouteMediaType } from "@/api/v2/adminRequests";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAdminRequestCapabilities } from "@/hooks/queries/admin/requests";
import { useT, type Translate } from "@/i18n";
import {
  ROUTING_COUNTRIES,
  ROUTING_DECADES,
  ROUTING_LANGUAGES,
  routingCountryName,
  routingLanguageName,
} from "@/lib/requestRoutingOptions";
import { tmdbGenreName, tmdbGenresFor } from "@/lib/tmdbGenres";

import { FieldError, ValuePicker, type Choice } from "./RequestRouteFields";
import {
  isListRow,
  ROUTING_RATINGS,
  rowField,
  rowsToConditions,
  ruleSentence,
  SERIES_STATUS_PRESETS,
  SERIES_STATUSES,
  seriesStatusLabel,
  seriesStatusPreset,
  type ConditionKind,
  type ConditionRow,
  type ListConditionRow,
  type RoutingNames,
} from "./requestRoutingModel";

export interface ConditionLookups {
  users: readonly { id: number; username: string }[];
  /** The curated networks (series) or studios (movies). */
  brands: readonly DiscoverBrandCard[];
  /** Why the curated list cannot be offered; a TMDB ID still can. */
  brandsHint?: string;
}

function kindLabel(t: Translate, kind: ConditionKind, mediaType: RequestRouteMediaType): string {
  switch (kind) {
    case "anime":
      return t("web.admin.settings.requests.conditions.kind.anime");
    case "genre":
      return t("web.admin.settings.requests.conditions.kind.genre");
    case "language":
      return t("web.admin.settings.requests.conditions.kind.language");
    case "country":
      return t("web.admin.settings.requests.conditions.kind.country");
    case "year":
      return t(
        mediaType === "series"
          ? "web.admin.settings.requests.conditions.kind.firstAired"
          : "web.admin.settings.requests.conditions.kind.releaseYear",
      );
    case "rating":
      return t("web.admin.settings.requests.conditions.kind.rating");
    case "status":
      return t("web.admin.settings.requests.conditions.kind.status");
    case "network":
      return t("web.admin.settings.requests.conditions.kind.network");
    case "studio":
      return t("web.admin.settings.requests.conditions.kind.studio");
    case "requester":
      return t("web.admin.settings.requests.conditions.kind.requester");
    case "keyword":
      return t("web.admin.settings.requests.conditions.kind.keyword");
  }
}

/**
 * The row a menu choice adds, or null when the kind has no room left: one
 * line for anime, years and rating; for a list, one "is any of" line and one
 * "is none of" line, the new one taking whichever mode is free.
 */
function newRow(kind: ConditionKind, rows: readonly ConditionRow[]): ConditionRow | null {
  const same = rows.filter((row) => row.kind === kind);
  if (kind === "anime") return same.length ? null : { kind, anime: true };
  if (kind === "year") return same.length ? null : { kind };
  if (kind === "rating") return same.length ? null : { kind, max: "PG" };
  const taken = same.filter(isListRow).map((row) => row.mode);
  const mode = !taken.includes("any") ? "any" : !taken.includes("none") ? "none" : null;
  return mode ? { kind, mode, values: [] } : null;
}

function parseYear(value: string): number | undefined {
  const year = Number.parseInt(value, 10);
  return Number.isFinite(year) && year > 0 ? year : undefined;
}

/** An input for a numeric TMDB ID, added with Enter or the button. */
export function IdAdder({ label, onAdd }: { label: string; onAdd: (id: string) => void }) {
  const t = useT();
  const [text, setText] = useState("");
  const id = Number(text.trim());
  const valid = Number.isInteger(id) && id > 0;
  function add() {
    if (!valid) return;
    onAdd(String(id));
    setText("");
  }
  return (
    <div className="flex items-center gap-1.5">
      <Input
        type="number"
        inputMode="numeric"
        min={1}
        aria-label={label}
        placeholder={t("web.admin.settings.requests.conditions.tmdbIdPlaceholder")}
        className="h-8 w-28 text-xs"
        value={text}
        onChange={(event) => setText(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            add();
          }
        }}
      />
      <Button type="button" size="sm" variant="outline" onClick={add} disabled={!valid}>
        {t("web.admin.settings.requests.conditions.addId")}
      </Button>
    </div>
  );
}

function ListValues({
  row,
  mediaType,
  lookups,
  onChange,
}: {
  row: ListConditionRow;
  mediaType: RequestRouteMediaType;
  lookups: ConditionLookups;
  onChange: (values: string[]) => void;
}) {
  const t = useT();
  const brandNames = new Map(
    lookups.brands
      .filter((brand) => brand.tmdb_id)
      .map((brand) => [String(brand.tmdb_id), brand.display_name]),
  );
  let options: Choice[] = [];
  let labelOf: (value: string) => string = (value) => value;
  let addLabel = t("web.admin.settings.requests.conditions.add");
  switch (row.kind) {
    case "genre":
      options = tmdbGenresFor(mediaType).map((genre) => ({
        value: String(genre.id),
        label: genre.name,
      }));
      labelOf = (value) => tmdbGenreName(Number(value), mediaType);
      addLabel = t("web.admin.settings.requests.conditions.addGenre");
      break;
    case "language":
      options = ROUTING_LANGUAGES.map((language) => ({
        value: language.code,
        label: language.name,
      }));
      labelOf = routingLanguageName;
      addLabel = t("web.admin.settings.requests.conditions.addLanguage");
      break;
    case "country":
      options = ROUTING_COUNTRIES.map((country) => ({ value: country.code, label: country.name }));
      labelOf = routingCountryName;
      addLabel = t("web.admin.settings.requests.conditions.addCountry");
      break;
    case "network":
    case "studio":
      options = [...brandNames].map(([value, label]) => ({ value, label }));
      labelOf = (value) =>
        brandNames.get(value) ??
        t("web.admin.settings.requests.conditions.tmdbFallback", { id: value });
      addLabel = t(
        row.kind === "network"
          ? "web.admin.settings.requests.conditions.addNetwork"
          : "web.admin.settings.requests.conditions.addStudio",
      );
      break;
    case "requester":
      options = lookups.users.map((user) => ({ value: String(user.id), label: user.username }));
      labelOf = (value) =>
        lookups.users.find((user) => String(user.id) === value)?.username ??
        t("web.admin.settings.requests.conditions.accountFallback", { id: value });
      addLabel = t("web.admin.settings.requests.conditions.addAccount");
      break;
    case "status":
      options = [...SERIES_STATUSES];
      labelOf = seriesStatusLabel;
      addLabel = t("web.admin.settings.requests.conditions.addStatus");
      break;
    case "keyword":
      labelOf = (value) =>
        t("web.admin.settings.requests.conditions.keywordFallback", { id: value });
      break;
  }
  const add = (value: string) => {
    if (!row.values.includes(value)) onChange([...row.values, value]);
  };
  const byId = row.kind === "network" || row.kind === "studio" || row.kind === "keyword";
  return (
    <div className="flex flex-col gap-2">
      <ValuePicker
        addLabel={addLabel}
        options={options}
        selected={row.values}
        labelOf={labelOf}
        onChange={onChange}
        hideAdd={row.kind === "keyword"}
        unavailableHint={
          row.kind === "network" || row.kind === "studio" ? lookups.brandsHint : undefined
        }
      />
      {row.kind === "status" ? (
        <div
          role="group"
          aria-label={t("web.admin.settings.requests.conditions.statusPresets")}
          className="flex flex-wrap gap-1.5"
        >
          {SERIES_STATUS_PRESETS.map((preset) => {
            const on = seriesStatusPreset(row.values) === preset;
            return (
              <Button
                key={preset.labelKey}
                type="button"
                size="xs"
                variant={on ? "default" : "outline"}
                aria-pressed={on}
                onClick={() => onChange(on ? [] : [...preset.values])}
              >
                {preset.label}
              </Button>
            );
          })}
        </div>
      ) : null}
      {byId ? (
        <div className="flex flex-wrap items-center gap-2">
          {row.kind !== "keyword" ? (
            <span className="text-muted-foreground text-xs">
              {t("web.admin.settings.requests.conditions.orEnterId")}
            </span>
          ) : null}
          <IdAdder
            label={
              row.kind === "keyword"
                ? t("web.admin.settings.requests.conditions.keywordIdLabel")
                : t("web.admin.settings.requests.conditions.idLabel", {
                    condition: kindLabel(t, row.kind, mediaType),
                  })
            }
            onAdd={add}
          />
        </div>
      ) : null}
    </div>
  );
}

function YearValues({
  row,
  onChange,
  invalid,
}: {
  row: Extract<ConditionRow, { kind: "year" }>;
  onChange: (row: Extract<ConditionRow, { kind: "year" }>) => void;
  invalid: boolean;
}) {
  const t = useT();
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="text-muted-foreground text-xs">
          {t("web.admin.settings.requests.conditions.yearBetween")}
        </span>
        <Input
          type="number"
          aria-label={t("web.admin.settings.requests.conditions.yearFromLabel")}
          placeholder={t("web.admin.settings.requests.conditions.yearFrom")}
          className="h-8 w-24"
          value={row.from ?? ""}
          onChange={(event) => onChange({ ...row, from: parseYear(event.target.value) })}
          aria-invalid={invalid}
        />
        <span className="text-muted-foreground text-xs">
          {t("web.admin.settings.requests.conditions.yearAnd")}
        </span>
        <Input
          type="number"
          aria-label={t("web.admin.settings.requests.conditions.yearToLabel")}
          placeholder={t("web.admin.settings.requests.conditions.yearTo")}
          className="h-8 w-24"
          value={row.to ?? ""}
          onChange={(event) => onChange({ ...row, to: parseYear(event.target.value) })}
          aria-invalid={invalid}
        />
      </div>
      <div
        role="group"
        aria-label={t("web.admin.settings.requests.conditions.decades")}
        className="flex flex-wrap gap-1.5"
      >
        {ROUTING_DECADES.map((decade) => {
          const on = row.from === decade.from && row.to === decade.to;
          return (
            <Button
              key={decade.label}
              type="button"
              size="xs"
              variant={on ? "default" : "outline"}
              aria-pressed={on}
              onClick={() =>
                onChange(on ? { kind: "year" } : { kind: "year", from: decade.from, to: decade.to })
              }
            >
              {decade.label}
            </Button>
          );
        })}
      </div>
    </div>
  );
}

function ConditionLine({
  row,
  mediaType,
  lookups,
  takenModes,
  error,
  onChange,
  onRemove,
}: {
  row: ConditionRow;
  mediaType: RequestRouteMediaType;
  lookups: ConditionLookups;
  /** Modes another row of the same kind already uses. */
  takenModes: readonly string[];
  error?: string;
  onChange: (row: ConditionRow) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const label = kindLabel(t, row.kind, mediaType);
  let control: ReactNode = null;
  let body: ReactNode = null;
  let hint: string | null = null;
  if (row.kind === "anime") {
    control = (
      <Select
        value={row.anime ? "yes" : "no"}
        onValueChange={(value) => onChange({ kind: "anime", anime: value === "yes" })}
      >
        <SelectTrigger size="sm" aria-label={label} className="w-40">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="yes">{t("web.admin.settings.requests.conditions.isAnime")}</SelectItem>
          <SelectItem value="no">
            {t("web.admin.settings.requests.conditions.isNotAnime")}
          </SelectItem>
        </SelectContent>
      </Select>
    );
  } else if (row.kind === "rating") {
    control = (
      <>
        <span className="text-muted-foreground text-xs">
          {t("web.admin.settings.requests.conditions.ratingAtMost")}
        </span>
        <Select value={row.max} onValueChange={(max) => onChange({ kind: "rating", max })}>
          <SelectTrigger
            size="sm"
            aria-label={t("web.admin.settings.requests.conditions.ratingLabel")}
            className="w-28"
          >
            <SelectValue
              placeholder={t("web.admin.settings.requests.conditions.ratingPlaceholder")}
            />
          </SelectTrigger>
          <SelectContent>
            {(ROUTING_RATINGS.includes(row.max) || !row.max
              ? ROUTING_RATINGS
              : [...ROUTING_RATINGS, row.max]
            ).map((rating) => (
              <SelectItem key={rating} value={rating}>
                {rating}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </>
    );
    hint = t("web.admin.settings.requests.conditions.ratingHint");
  } else if (row.kind === "year") {
    body = <YearValues row={row} onChange={onChange} invalid={Boolean(error)} />;
  } else {
    if (row.kind === "status") {
      hint = t("web.admin.settings.requests.conditions.statusHint", { mode: row.mode });
    }
    control = (
      <Select
        value={row.mode}
        onValueChange={(mode) => onChange({ ...row, mode: mode as ListConditionRow["mode"] })}
      >
        <SelectTrigger
          size="sm"
          aria-label={t("web.admin.settings.requests.conditions.matchLabel", { condition: label })}
          className="w-32"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="any" disabled={takenModes.includes("any")}>
            {t("web.admin.settings.requests.conditions.isAnyOf")}
          </SelectItem>
          <SelectItem value="none" disabled={takenModes.includes("none")}>
            {t("web.admin.settings.requests.conditions.isNoneOf")}
          </SelectItem>
        </SelectContent>
      </Select>
    );
    body = (
      <ListValues
        row={row}
        mediaType={mediaType}
        lookups={lookups}
        onChange={(values) => onChange({ ...row, values })}
      />
    );
  }
  return (
    <li
      aria-label={t("web.admin.settings.requests.conditions.conditionLabel", { condition: label })}
      className="border-border/70 bg-foreground/[0.02] space-y-2 rounded-lg border px-3 py-2.5"
    >
      <div className="flex items-start gap-2">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <span className="min-w-28 text-sm font-medium">{label}</span>
          {control}
        </div>
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          className="shrink-0"
          aria-label={t("web.admin.settings.requests.conditions.remove", {
            condition: label.toLowerCase(),
          })}
          onClick={onRemove}
        >
          <X />
        </Button>
      </div>
      {body}
      {hint ? <p className="text-muted-foreground text-xs">{hint}</p> : null}
      <FieldError>{error}</FieldError>
    </li>
  );
}

/** Names for the summary: accounts, and the media type's networks or studios. */
function conditionNames(lookups: ConditionLookups, mediaType: RequestRouteMediaType): RoutingNames {
  const brands = new Map(
    lookups.brands.flatMap((brand) =>
      brand.tmdb_id ? [[brand.tmdb_id, brand.display_name] as const] : [],
    ),
  );
  return {
    users: new Map(lookups.users.map((user) => [user.id, user.username])),
    ...(mediaType === "series" ? { networks: brands } : { studios: brands }),
  };
}

const MENU_KINDS: readonly ConditionKind[] = [
  "anime",
  "genre",
  "language",
  "country",
  "year",
  "rating",
];

/**
 * "Which requests": the rule's conditions, one line each, and a menu to add
 * another. Only the conditions in use are shown.
 */
export function RuleConditionsEditor({
  mediaType,
  rows,
  onChange,
  lookups,
  errors,
}: {
  mediaType: RequestRouteMediaType;
  rows: readonly ConditionRow[];
  onChange: (rows: ConditionRow[]) => void;
  lookups: ConditionLookups;
  /** The editor's field errors, keyed like the API's (`conditions.genre_ids`). */
  errors: Record<string, string>;
}) {
  const t = useT();
  const headingId = useId();
  const brandKind: ConditionKind = mediaType === "series" ? "network" : "studio";
  // A server without the status condition refuses it in a save and ignores
  // it in a rule another server saved, so it is offered only where the server
  // says it takes it. A saved status row still shows.
  const statusSupported = useAdminRequestCapabilities().data?.routing_series_status ?? false;
  const menu: ConditionKind[] = [
    ...MENU_KINDS,
    ...(mediaType === "series" && statusSupported ? (["status"] as const) : []),
    brandKind,
    "requester",
  ];
  // The rule in words, as the list shows it: "When a movie is Action or
  // Documentary and came out in 1970–1979".
  const conditions = rowsToConditions(rows);
  const summary =
    Object.keys(conditions).length > 0
      ? t("web.admin.settings.requests.conditions.summary", {
          sentence: ruleSentence(conditions, mediaType, conditionNames(lookups, mediaType)),
        })
      : "";
  const add = (kind: ConditionKind) => {
    const row = newRow(kind, rows);
    if (row) onChange([...rows, row]);
  };

  function errorFor(row: ConditionRow): string | undefined {
    if (row.kind === "year") {
      return errors["conditions.year_from"] ?? errors["conditions.year_to"];
    }
    return errors[`conditions.${rowField(row)}`];
  }

  return (
    <section aria-labelledby={headingId} className="space-y-2">
      <div>
        <h3 id={headingId} className="text-sm font-semibold">
          {t("web.admin.settings.requests.conditions.heading")}
        </h3>
        <p className="text-muted-foreground text-xs leading-relaxed">
          {t("web.admin.settings.requests.conditions.description")}
        </p>
      </div>
      <FieldError>{errors.conditions}</FieldError>
      {rows.length > 0 ? (
        <ul className="flex list-none flex-col gap-1">
          {rows.map((row, index) => (
            <Fragment key={`${row.kind}-${isListRow(row) ? row.mode : ""}`}>
              {index > 0 ? (
                <li
                  aria-hidden="true"
                  className="text-muted-foreground px-3 text-xs font-semibold tracking-wide uppercase"
                >
                  {t("web.admin.settings.requests.conditions.and")}
                </li>
              ) : null}
              <ConditionLine
                row={row}
                mediaType={mediaType}
                lookups={lookups}
                takenModes={rows
                  .filter((other, i) => i !== index && other.kind === row.kind && isListRow(other))
                  .map((other) => (other as ListConditionRow).mode)}
                error={errorFor(row)}
                onChange={(next) =>
                  onChange(rows.map((current, i) => (i === index ? next : current)))
                }
                onRemove={() => onChange(rows.filter((_, i) => i !== index))}
              />
            </Fragment>
          ))}
        </ul>
      ) : null}
      {summary ? (
        <p
          aria-live="polite"
          className="border-border/70 text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-xs"
        >
          {t("web.admin.settings.requests.conditions.takes", {
            summary,
            label: (chunks) => <span className="text-foreground font-medium">{chunks}</span>,
          })}
        </p>
      ) : null}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button type="button" size="sm" variant="outline">
            <Plus aria-hidden="true" />
            {t("web.admin.settings.requests.conditions.addCondition")}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          {menu.map((kind) => (
            <DropdownMenuItem
              key={kind}
              disabled={newRow(kind, rows) === null}
              onSelect={() => add(kind)}
            >
              {kindLabel(t, kind, mediaType)}
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuLabel className="text-muted-foreground text-xs font-normal">
            {t("web.admin.settings.requests.conditions.advanced")}
          </DropdownMenuLabel>
          <DropdownMenuItem
            disabled={newRow("keyword", rows) === null}
            onSelect={() => add("keyword")}
          >
            {kindLabel(t, "keyword", mediaType)}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </section>
  );
}
