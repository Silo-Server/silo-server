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

function kindLabel(kind: ConditionKind, mediaType: RequestRouteMediaType): string {
  switch (kind) {
    case "anime":
      return "Anime";
    case "genre":
      return "Genre";
    case "language":
      return "Original language";
    case "country":
      return "Country of origin";
    case "year":
      return mediaType === "series" ? "First aired" : "Release year";
    case "rating":
      return "Content rating";
    case "network":
      return "Network";
    case "studio":
      return "Studio";
    case "requester":
      return "Requested by";
    case "keyword":
      return "TMDB keyword";
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
function IdAdder({ label, onAdd }: { label: string; onAdd: (id: string) => void }) {
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
        placeholder="TMDB ID"
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
        Add
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
  const brandNames = new Map(
    lookups.brands
      .filter((brand) => brand.tmdb_id)
      .map((brand) => [String(brand.tmdb_id), brand.display_name]),
  );
  let options: Choice[] = [];
  let labelOf: (value: string) => string = (value) => value;
  let addLabel = "Add";
  switch (row.kind) {
    case "genre":
      options = tmdbGenresFor(mediaType).map((genre) => ({
        value: String(genre.id),
        label: genre.name,
      }));
      labelOf = (value) => tmdbGenreName(Number(value), mediaType);
      addLabel = "Add a genre";
      break;
    case "language":
      options = ROUTING_LANGUAGES.map((language) => ({
        value: language.code,
        label: language.name,
      }));
      labelOf = routingLanguageName;
      addLabel = "Add a language";
      break;
    case "country":
      options = ROUTING_COUNTRIES.map((country) => ({ value: country.code, label: country.name }));
      labelOf = routingCountryName;
      addLabel = "Add a country";
      break;
    case "network":
    case "studio":
      options = [...brandNames].map(([value, label]) => ({ value, label }));
      labelOf = (value) => brandNames.get(value) ?? `TMDB ${value}`;
      addLabel = `Add a ${row.kind}`;
      break;
    case "requester":
      options = lookups.users.map((user) => ({ value: String(user.id), label: user.username }));
      labelOf = (value) =>
        lookups.users.find((user) => String(user.id) === value)?.username ?? `Account ${value}`;
      addLabel = "Add an account";
      break;
    case "keyword":
      labelOf = (value) => `Keyword ${value}`;
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
      {byId ? (
        <div className="flex flex-wrap items-center gap-2">
          {row.kind !== "keyword" ? (
            <span className="text-muted-foreground text-xs">Or enter a TMDB ID</span>
          ) : null}
          <IdAdder
            label={
              row.kind === "keyword"
                ? "TMDB keyword ID"
                : `${kindLabel(row.kind, mediaType)} TMDB ID`
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
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="text-muted-foreground text-xs">between</span>
        <Input
          type="number"
          aria-label="From year"
          placeholder="From"
          className="h-8 w-24"
          value={row.from ?? ""}
          onChange={(event) => onChange({ ...row, from: parseYear(event.target.value) })}
          aria-invalid={invalid}
        />
        <span className="text-muted-foreground text-xs">and</span>
        <Input
          type="number"
          aria-label="To year"
          placeholder="To"
          className="h-8 w-24"
          value={row.to ?? ""}
          onChange={(event) => onChange({ ...row, to: parseYear(event.target.value) })}
          aria-invalid={invalid}
        />
      </div>
      <div role="group" aria-label="Decades" className="flex flex-wrap gap-1.5">
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
  const label = kindLabel(row.kind, mediaType);
  let control: ReactNode = null;
  let body: ReactNode = null;
  let hint: string | null = null;
  if (row.kind === "anime") {
    control = (
      <Select
        value={row.anime ? "yes" : "no"}
        onValueChange={(value) => onChange({ kind: "anime", anime: value === "yes" })}
      >
        <SelectTrigger size="sm" aria-label="Anime" className="w-40">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="yes">is anime</SelectItem>
          <SelectItem value="no">isn&apos;t anime</SelectItem>
        </SelectContent>
      </Select>
    );
  } else if (row.kind === "rating") {
    control = (
      <>
        <span className="text-muted-foreground text-xs">is at most</span>
        <Select value={row.max} onValueChange={(max) => onChange({ kind: "rating", max })}>
          <SelectTrigger size="sm" aria-label="Highest content rating" className="w-28">
            <SelectValue placeholder="Rating" />
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
    hint = "Titles without a US rating use their own country's; titles with neither don't match.";
  } else if (row.kind === "year") {
    body = <YearValues row={row} onChange={onChange} invalid={Boolean(error)} />;
  } else {
    control = (
      <Select
        value={row.mode}
        onValueChange={(mode) => onChange({ ...row, mode: mode as ListConditionRow["mode"] })}
      >
        <SelectTrigger size="sm" aria-label={`${label} match`} className="w-32">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="any" disabled={takenModes.includes("any")}>
            is any of
          </SelectItem>
          <SelectItem value="none" disabled={takenModes.includes("none")}>
            is none of
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
      aria-label={`${label} condition`}
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
          aria-label={`Remove the ${label.toLowerCase()} condition`}
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
  const headingId = useId();
  const brandKind: ConditionKind = mediaType === "series" ? "network" : "studio";
  const menu: ConditionKind[] = [...MENU_KINDS, brandKind, "requester"];
  // The rule in words, as the list shows it: "When a movie is Action or
  // Documentary and came out in 1970–1979".
  const conditions = rowsToConditions(rows);
  const summary =
    Object.keys(conditions).length > 0
      ? `${ruleSentence(conditions, mediaType, conditionNames(lookups, mediaType))}.`
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
          Which requests
        </h3>
        <p className="text-muted-foreground text-xs leading-relaxed">
          A request has to match every condition. Where a condition lists several values, one of
          them is enough.
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
                  className="text-muted-foreground px-3 text-[11px] font-semibold tracking-wide uppercase"
                >
                  and
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
          <span className="text-foreground font-medium">Takes: </span>
          {summary}
        </p>
      ) : null}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button type="button" size="sm" variant="outline">
            <Plus aria-hidden="true" />
            Add condition
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          {menu.map((kind) => (
            <DropdownMenuItem
              key={kind}
              disabled={newRow(kind, rows) === null}
              onSelect={() => add(kind)}
            >
              {kindLabel(kind, mediaType)}
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuLabel className="text-muted-foreground text-xs font-normal">
            Advanced
          </DropdownMenuLabel>
          <DropdownMenuItem
            disabled={newRow("keyword", rows) === null}
            onSelect={() => add("keyword")}
          >
            TMDB keyword
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </section>
  );
}
