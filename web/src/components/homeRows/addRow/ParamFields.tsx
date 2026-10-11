import { useId, useState, type ReactNode } from "react";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { sectionLibraryFilterIds, withSectionLibraryFilterIds } from "@/lib/sectionLibraryFilter";
import { hasParamFields } from "@/lib/homeRows/paramFields";
import { SEASONAL_THEME_LABELS } from "@/lib/homeRows/variants";

type Config = Record<string, unknown>;

export interface ParamLibrary {
  id: number;
  name: string;
  type?: string;
}

interface FieldProps {
  config: Config;
  onChange: (next: Config) => void;
}

function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor?: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint ? <p className="text-muted-foreground text-[13px]">{hint}</p> : null}
    </div>
  );
}

function LibraryFilterField({
  config,
  onChange,
  libraries,
}: FieldProps & { libraries: ParamLibrary[] }) {
  return (
    <Field label="From" hint="Which libraries the row takes titles from.">
      <LibraryMultiSelect
        libraries={libraries}
        emptyLabel="All libraries"
        value={sectionLibraryFilterIds(config)}
        onChange={(ids) => onChange(withSectionLibraryFilterIds(config, ids))}
      />
    </Field>
  );
}

const MEDIA_TYPES = [
  { value: "all", label: "Everything" },
  { value: "movie", label: "Movies" },
  { value: "series", label: "TV shows" },
  { value: "audiobook", label: "Audiobooks" },
];

// "default" keeps the list's stored order: provider sync order, newest added first.
const LIST_ORDERS = [
  { value: "default", label: "List order" },
  { value: "added_at:desc", label: "Newest added first" },
  { value: "added_at:asc", label: "Oldest added first" },
  { value: "title:asc", label: "A to Z" },
  { value: "title:desc", label: "Z to A" },
  { value: "release_date:desc", label: "Newest released first" },
  { value: "release_date:asc", label: "Oldest released first" },
  { value: "rating_imdb:desc", label: "Highest IMDb rating first" },
];

function listOrderValue(config: Config): string {
  const sort = typeof config.sort === "string" ? config.sort : "";
  if (!sort) return "default";
  // The server's per-field default: titles A to Z, everything else descending.
  const order =
    typeof config.order === "string" && config.order
      ? config.order
      : sort === "title"
        ? "asc"
        : "desc";
  const value = `${sort}:${order}`;
  return LIST_ORDERS.some((option) => option.value === value) ? value : "default";
}

function PersonalListFields({
  config,
  onChange,
  libraries,
}: FieldProps & { libraries: ParamLibrary[] }) {
  const id = useId();
  const filterType =
    typeof config.filter_type === "string" && config.filter_type ? config.filter_type : "all";
  const libraryIds = Array.isArray(config.filter_library_ids)
    ? config.filter_library_ids.filter((value): value is number => typeof value === "number")
    : [];
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label="Media type" htmlFor={`${id}-type`}>
        <Select
          value={filterType}
          onValueChange={(value) =>
            onChange({ ...config, filter_type: value === "all" ? undefined : value })
          }
        >
          <SelectTrigger id={`${id}-type`} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {MEDIA_TYPES.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field label="Libraries">
        <LibraryMultiSelect
          libraries={libraries}
          emptyLabel="All libraries"
          value={libraryIds}
          onChange={(ids) =>
            onChange({ ...config, filter_library_ids: ids.length > 0 ? ids : undefined })
          }
        />
      </Field>
      <Field label="Order" htmlFor={`${id}-order`}>
        <Select
          value={listOrderValue(config)}
          onValueChange={(value) => {
            const [sort, order] = value === "default" ? [] : value.split(":");
            onChange({ ...config, sort: sort || undefined, order: order || undefined });
          }}
        >
          <SelectTrigger id={`${id}-order`} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {LIST_ORDERS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
    </div>
  );
}

/** One optional whole-number setting; clearing it lets the server default apply. */
function NumberField({
  config,
  onChange,
  paramKey,
  label,
  placeholder,
  hint,
}: FieldProps & { paramKey: string; label: string; placeholder: string; hint: string }) {
  const id = useId();
  const raw = config[paramKey];
  return (
    <Field label={label} htmlFor={id} hint={hint}>
      <Input
        id={id}
        type="number"
        min={1}
        step={1}
        inputMode="numeric"
        className="w-32"
        placeholder={placeholder}
        value={typeof raw === "number" && Number.isFinite(raw) ? String(raw) : ""}
        onChange={(event) => {
          const parsed = Number(event.target.value);
          // The server reads these as whole numbers; a fraction would fail to save.
          onChange({
            ...config,
            [paramKey]:
              event.target.value && Number.isInteger(parsed) && parsed > 0 ? parsed : undefined,
          });
        }}
      />
    </Field>
  );
}

function GenreField({ config, onChange }: FieldProps) {
  const id = useId();
  return (
    <Field
      label="Genre (optional)"
      htmlFor={id}
      hint="Leave blank to follow each viewer's strongest genre."
    >
      <Input
        id={id}
        value={typeof config.genre === "string" ? config.genre : ""}
        onChange={(event) => onChange({ ...config, genre: event.target.value })}
      />
    </Field>
  );
}

const CADENCES = [
  { value: "daily", label: "Every day" },
  { value: "weekly", label: "Every week" },
  { value: "monthly", label: "Every month" },
];

function SpotlightFields({ config, onChange }: FieldProps) {
  const id = useId();
  // A preset with a pinned subject and no auto_rotate is pinned, as the
  // server reads it; defaulting the switch on would hide the subject.
  const rotating = typeof config.auto_rotate === "boolean" ? config.auto_rotate : !config.subject;
  const cadence = typeof config.rotation_cadence === "string" ? config.rotation_cadence : "weekly";
  return (
    <div className="grid gap-4">
      <div className="flex items-center justify-between gap-4">
        <Label htmlFor={`${id}-rotate`}>Pick a new subject automatically</Label>
        <Switch
          id={`${id}-rotate`}
          checked={rotating}
          onCheckedChange={(checked) => onChange({ ...config, auto_rotate: checked === true })}
        />
      </div>
      {rotating ? (
        <Field label="How often" htmlFor={`${id}-cadence`}>
          <Select
            value={cadence}
            onValueChange={(value) => onChange({ ...config, rotation_cadence: value })}
          >
            <SelectTrigger id={`${id}-cadence`} className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {CADENCES.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      ) : (
        <Field label="Subject" htmlFor={`${id}-subject`}>
          <Input
            id={`${id}-subject`}
            placeholder={config.subject_type === "era" ? "e.g. 1990s" : "e.g. Christopher Nolan"}
            value={typeof config.subject === "string" ? config.subject : ""}
            onChange={(event) => onChange({ ...config, subject: event.target.value })}
          />
        </Field>
      )}
    </div>
  );
}

// Order matches SeasonalThemeOrder on the server: earlier holidays win when
// several are in season at once. Family movie night is its own variant, so it
// isn't offered here; a stored one stays in the list.
const HOLIDAYS = [
  { key: "valentines", when: "Feb 7–14" },
  { key: "st_patricks", when: "Mar 15–17" },
  { key: "thanksgiving", when: "Nov 22–30" },
  { key: "christmas", when: "December" },
  { key: "halloween", when: "October" },
  { key: "saturday_morning", when: "Saturdays before 1pm" },
  { key: "summer_blockbuster", when: "June to August" },
].map((holiday) => ({ ...holiday, label: SEASONAL_THEME_LABELS[holiday.key]! }));

function enabledHolidays(config: Config): string[] {
  if (Array.isArray(config.enabled_themes)) {
    return config.enabled_themes.filter((value): value is string => typeof value === "string");
  }
  // A legacy single-holiday row starts from its one holiday.
  return typeof config.theme === "string" && config.theme ? [config.theme] : [];
}

function HolidayChecklist({ config, onChange }: FieldProps) {
  const id = useId();
  // The name box being typed in, as typed: the draft gets it trimmed, so a
  // space typed between words would otherwise vanish before the next word.
  const [typed, setTyped] = useState<{ key: string; text: string } | null>(null);
  const enabled = new Set(enabledHolidays(config));
  const titles: Record<string, string> = {};
  if (config.theme_titles && typeof config.theme_titles === "object") {
    for (const [key, value] of Object.entries(config.theme_titles)) {
      if (typeof value === "string") titles[key] = value;
    }
  }

  function commit(nextEnabled: Set<string>, nextTitles: Record<string, string>) {
    // Keep only titles for holidays that are on, so the saved config stays tidy.
    const kept = Object.fromEntries(
      Object.entries(nextTitles)
        .filter(([key]) => nextEnabled.has(key))
        .map(([key, value]) => [key, value.trim()] as const)
        .filter(([, value]) => value !== ""),
    );
    onChange({
      ...config,
      enabled_themes: Array.from(nextEnabled),
      theme_titles: Object.keys(kept).length > 0 ? kept : undefined,
      // Legacy single-theme keys would shadow the list on the server.
      theme: "",
      mode: "",
    });
  }

  return (
    <fieldset className="grid gap-2">
      <legend className="mb-2 text-sm font-medium">Holidays</legend>
      <div className="border-border divide-border/70 divide-y rounded-xl border">
        {HOLIDAYS.map((holiday) => {
          const on = enabled.has(holiday.key);
          const checkboxId = `${id}-${holiday.key}`;
          return (
            <div key={holiday.key} className="grid gap-2 px-3.5 py-2.5">
              <div className="flex items-center gap-3">
                <Checkbox
                  id={checkboxId}
                  checked={on}
                  onCheckedChange={(checked) => {
                    const next = new Set(enabled);
                    if (checked === true) next.add(holiday.key);
                    else next.delete(holiday.key);
                    commit(next, titles);
                  }}
                />
                <Label htmlFor={checkboxId} className="font-normal">
                  {holiday.label}
                </Label>
                <span className="text-muted-foreground ml-auto text-xs">{holiday.when}</span>
              </div>
              {on ? (
                <Input
                  aria-label={`Row name during ${holiday.label}`}
                  placeholder={`Name in season (defaults to "${holiday.label}")`}
                  className="h-8 text-[13px]"
                  value={typed?.key === holiday.key ? typed.text : (titles[holiday.key] ?? "")}
                  onChange={(event) => {
                    setTyped({ key: holiday.key, text: event.target.value });
                    commit(enabled, { ...titles, [holiday.key]: event.target.value });
                  }}
                  onBlur={() => setTyped(null)}
                />
              ) : null}
            </div>
          );
        })}
      </div>
      {HOLIDAYS.some((holiday) => enabled.has(holiday.key)) ? (
        <p className="text-muted-foreground text-[13px]">
          The row shows whichever holiday is in season and hides itself when none is.
        </p>
      ) : (
        <p role="status" className="text-destructive text-[13px]">
          Pick at least one holiday.
        </p>
      )}
    </fieldset>
  );
}

/**
 * The plain settings a ready-made row keeps, in one of two places: next to
 * the variant ("primary") or under More options ("more"). The raw Anchor item
 * box is gone; a stored anchor stays in the config untouched.
 */
export function ParamFields({
  sectionType,
  config,
  onChange,
  slot,
  libraries,
  onLibraryPage,
}: FieldProps & {
  sectionType: string;
  slot: "primary" | "more";
  libraries: ParamLibrary[];
  onLibraryPage: boolean;
}) {
  if (!hasParamFields(sectionType, slot, config, onLibraryPage)) return null;
  const props = { config, onChange };
  if (slot === "primary") {
    return sectionType === "seasonal_themed" ? (
      <HolidayChecklist {...props} />
    ) : (
      <LibraryFilterField {...props} libraries={libraries} />
    );
  }
  switch (sectionType) {
    case "watchlist":
    case "favorites":
      return <PersonalListFields {...props} libraries={libraries} />;
    case "returning_shows":
      return (
        <NumberField
          {...props}
          paramKey="lookback_days"
          label="Look back (days)"
          placeholder="30"
          hint="How recently a new season must have arrived."
        />
      );
    case "short_watches":
      return (
        <NumberField
          {...props}
          paramKey="max_minutes"
          label="Longest runtime (minutes)"
          placeholder="95"
          hint="Movies at or under this runtime qualify."
        />
      );
    case "anniversaries":
      return (
        <NumberField
          {...props}
          paramKey="milestone_years"
          label="Milestone (years)"
          placeholder="5"
          hint="Only anniversaries that are a multiple of this. 1 shows every anniversary."
        />
      );
    case "taste_match":
      return <GenreField {...props} />;
    case "editorial_spotlight":
      return <SpotlightFields {...props} />;
    default:
      return null;
  }
}
