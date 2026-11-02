import { useId, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
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
import {
  useSeasonalThemes,
  type SeasonalTheme,
  type SeasonalVibe,
} from "@/hooks/queries/seasonalThemes";
import {
  customHolidayProblem,
  customHolidaysOf,
  holidayDatesProblem,
  holidayDatesLabel,
  themeDatesOf,
  type CustomHoliday,
  type HolidayDates,
} from "@/lib/homeRows/seasonal";

type Config = Record<string, unknown>;

interface FieldProps {
  config: Config;
  onChange: (next: Config) => void;
}

/** Family movie night is its own variant, so the holiday list leaves it out. */
const FAMILY_NIGHT = "family_movie_night";

function enabledHolidays(config: Config): string[] {
  if (Array.isArray(config.enabled_themes)) {
    return config.enabled_themes.filter((value): value is string => typeof value === "string");
  }
  // A legacy single-holiday row starts from its one holiday.
  return typeof config.theme === "string" && config.theme ? [config.theme] : [];
}

function stringMap(value: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (value && typeof value === "object") {
    for (const [key, entry] of Object.entries(value)) {
      if (typeof entry === "string") out[key] = entry;
    }
  }
  return out;
}

function chosenVibe(theme: SeasonalTheme, vibes: Record<string, string>): SeasonalVibe {
  return theme.vibes.find((vibe) => vibe.key === vibes[theme.key]) ?? theme.vibes[0]!;
}

function CatalogStatus({ isError }: { isError: boolean }) {
  return (
    <p role="status" className="text-muted-foreground text-[13px]">
      {isError ? "Couldn't load the holidays. Close and try again." : "Loading holidays…"}
    </p>
  );
}

function VibeSelect({
  theme,
  value,
  onChange,
}: {
  theme: SeasonalTheme;
  value: SeasonalVibe;
  onChange: (key: string) => void;
}) {
  return (
    <div className="grid gap-1">
      <Select value={value.key} onValueChange={onChange}>
        <SelectTrigger aria-label={`Vibe for ${theme.name}`} className="h-8 w-full text-[13px]">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {theme.vibes.map((vibe) => (
            <SelectItem key={vibe.key} value={vibe.key}>
              {vibe.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-muted-foreground text-xs">{value.description}</p>
    </div>
  );
}

function MonthDayInput({
  label,
  placeholder,
  value,
  onChange,
}: {
  label: string;
  placeholder: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <Input
      aria-label={label}
      placeholder={placeholder}
      inputMode="numeric"
      maxLength={5}
      className="h-8 w-24 text-[13px]"
      value={value}
      onChange={(event) => onChange(event.target.value)}
    />
  );
}

/** A holiday's own dates; empty keeps its usual window. */
function HolidayDatesFields({
  theme,
  dates,
  onChange,
}: {
  theme: SeasonalTheme;
  dates: HolidayDates | undefined;
  onChange: (next: HolidayDates | undefined) => void;
}) {
  const value = dates ?? { start: "", end: "" };
  const set = (patch: Partial<HolidayDates>) => {
    const next = { ...value, ...patch };
    onChange(next.start === "" && next.end === "" ? undefined : next);
  };
  const problem = dates ? holidayDatesProblem(dates) : null;
  return (
    <div className="grid gap-1">
      {/* On the narrowest phones the dates wrap under "In season", kept together. */}
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5 text-[13px]">
        <span className="text-muted-foreground">In season</span>
        <span className="flex items-center gap-2">
          <MonthDayInput
            label={`${theme.name} starts (MM-DD)`}
            placeholder="MM-DD"
            value={value.start}
            onChange={(start) => set({ start })}
          />
          <span className="text-muted-foreground">to</span>
          <MonthDayInput
            label={`${theme.name} ends (MM-DD)`}
            placeholder="MM-DD"
            value={value.end}
            onChange={(end) => set({ end })}
          />
        </span>
      </div>
      {problem ? (
        <p className="text-destructive text-xs">{problem}</p>
      ) : (
        <p className="text-muted-foreground text-xs">
          {dates
            ? "Every year on these dates."
            : `Leave empty for the usual dates (${theme.when}).`}
        </p>
      )}
    </div>
  );
}

const SOURCES = [
  {
    value: "recommendations",
    label: "Silo recommendations",
    hint: "The list's titles you have, most popular first, then similar titles from your library.",
  },
  {
    value: "lists",
    label: "The lists only",
    hint: "Only the list's titles you have, most popular first.",
  },
];

function SourceField({ config, onChange }: FieldProps) {
  const id = useId();
  const value = config.source === "lists" ? "lists" : "recommendations";
  const hint = SOURCES.find((source) => source.value === value)!.hint;
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={`${id}-source`}>Picks from</Label>
      <Select
        value={value}
        onValueChange={(next) =>
          onChange({ ...config, source: next === "recommendations" ? undefined : next })
        }
      >
        <SelectTrigger id={`${id}-source`} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {SOURCES.map((source) => (
            <SelectItem key={source.value} value={source.value}>
              {source.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-muted-foreground text-[13px]">{hint}</p>
    </div>
  );
}

function CustomHolidays({ config, onChange, max }: FieldProps & { max: number }) {
  const holidays = customHolidaysOf(config);
  const commit = (next: CustomHoliday[]) =>
    onChange({ ...config, custom_holidays: next.length > 0 ? next : undefined });
  const update = (index: number, patch: Partial<CustomHoliday>) =>
    commit(holidays.map((holiday, i) => (i === index ? { ...holiday, ...patch } : holiday)));

  return (
    <fieldset className="grid gap-2">
      <legend className="mb-1 text-sm font-medium">Your holidays</legend>
      <p className="text-muted-foreground text-[13px]">
        Add a holiday of your own with any public MDBList list. It shows ahead of the built-in
        holidays while it's in season.
      </p>
      {holidays.map((holiday, index) => {
        const label = holiday.name.trim() || `Holiday ${index + 1}`;
        const problem = customHolidayProblem(holiday);
        return (
          <div key={index} className="border-border grid gap-2 rounded-xl border px-3.5 py-3">
            <div className="flex items-center gap-2">
              <Input
                aria-label={`Name of holiday ${index + 1}`}
                placeholder="Name, e.g. Fourth of July"
                className="h-8 text-[13px]"
                value={holiday.name}
                onChange={(event) => update(index, { name: event.target.value })}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={`Remove ${label}`}
                onClick={() => commit(holidays.filter((_, i) => i !== index))}
              >
                <Trash2 className="size-4" />
              </Button>
            </div>
            <div className="flex items-center gap-2 text-[13px]">
              <MonthDayInput
                label={`${label} starts (MM-DD)`}
                placeholder="MM-DD"
                value={holiday.start}
                onChange={(start) => update(index, { start })}
              />
              <span className="text-muted-foreground">to</span>
              <MonthDayInput
                label={`${label} ends (MM-DD)`}
                placeholder="MM-DD"
                value={holiday.end}
                onChange={(end) => update(index, { end })}
              />
            </div>
            <Input
              aria-label={`${label} MDBList list`}
              placeholder="https://mdblist.com/lists/…"
              className="h-8 text-[13px]"
              value={holiday.list_url}
              onChange={(event) => update(index, { list_url: event.target.value })}
            />
            {problem ? <p className="text-destructive text-xs">{problem}</p> : null}
          </div>
        );
      })}
      {holidays.length < max ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="justify-self-start"
          onClick={() => commit([...holidays, { name: "", start: "", end: "", list_url: "" }])}
        >
          <Plus className="size-4" /> Add a holiday
        </Button>
      ) : null}
    </fieldset>
  );
}

/** The Holidays variant: which holidays, a vibe and name for each, and your own holidays. */
export function HolidayFields({ config, onChange }: FieldProps) {
  const id = useId();
  const catalog = useSeasonalThemes();
  // The name box being typed in, as typed: the draft gets it trimmed, so a
  // space typed between words would otherwise vanish before the next word.
  const [typed, setTyped] = useState<{ key: string; text: string } | null>(null);
  if (!catalog.data) return <CatalogStatus isError={catalog.isError} />;

  const themes = catalog.data.themes.filter((theme) => theme.key !== FAMILY_NIGHT);
  const enabled = new Set(enabledHolidays(config));
  const titles = stringMap(config.theme_titles);
  const vibes = stringMap(config.vibes);
  const dates = themeDatesOf(config);
  const hasCustom = customHolidaysOf(config).length > 0;

  function commit(
    nextEnabled: Set<string>,
    nextTitles: Record<string, string>,
    nextVibes = vibes,
    nextDates = dates,
  ) {
    // Keep only titles, vibes and dates for holidays that are on, so the saved config stays tidy.
    const forEnabled = <T,>(entries: Record<string, T>) =>
      Object.entries(entries).filter(([key]) => nextEnabled.has(key));
    const orUndefined = <T,>(entries: [string, T][]) =>
      entries.length > 0 ? Object.fromEntries(entries) : undefined;
    onChange({
      ...config,
      enabled_themes: Array.from(nextEnabled),
      theme_titles: orUndefined(
        forEnabled(nextTitles)
          .map(([key, value]) => [key, value.trim()] as [string, string])
          .filter(([, value]) => value !== ""),
      ),
      vibes: orUndefined(forEnabled(nextVibes)),
      theme_dates: orUndefined(forEnabled(nextDates)),
      // Legacy single-theme keys would shadow the list on the server.
      theme: "",
      mode: "",
    });
  }

  return (
    <div className="grid gap-5">
      <fieldset className="grid gap-2">
        <legend className="mb-2 text-sm font-medium">Holidays</legend>
        <div className="border-border divide-border/70 divide-y rounded-xl border">
          {themes.map((theme) => {
            const on = enabled.has(theme.key);
            const checkboxId = `${id}-${theme.key}`;
            const vibe = chosenVibe(theme, vibes);
            const own = dates[theme.key];
            const when = own && !holidayDatesProblem(own) ? holidayDatesLabel(own) : theme.when;
            return (
              <div key={theme.key} className="grid gap-2 px-3.5 py-2.5">
                <div className="flex items-center gap-3">
                  <Checkbox
                    id={checkboxId}
                    checked={on}
                    onCheckedChange={(checked) => {
                      const next = new Set(enabled);
                      if (checked === true) next.add(theme.key);
                      else next.delete(theme.key);
                      commit(next, titles);
                    }}
                  />
                  <Label htmlFor={checkboxId} className="font-normal">
                    {theme.name}
                  </Label>
                  <span className="text-muted-foreground ml-auto text-xs">{when}</span>
                </div>
                {on ? (
                  <div className="grid gap-2 pl-7">
                    {theme.vibes.length > 1 ? (
                      <VibeSelect
                        theme={theme}
                        value={vibe}
                        onChange={(key) => commit(enabled, titles, { ...vibes, [theme.key]: key })}
                      />
                    ) : null}
                    <Input
                      aria-label={`Row name during ${theme.name}`}
                      placeholder={`Name in season (defaults to "${vibe.title}")`}
                      className="h-8 text-[13px]"
                      value={typed?.key === theme.key ? typed.text : (titles[theme.key] ?? "")}
                      onChange={(event) => {
                        setTyped({ key: theme.key, text: event.target.value });
                        commit(enabled, { ...titles, [theme.key]: event.target.value });
                      }}
                      onBlur={() => setTyped(null)}
                    />
                    {theme.dates_editable ? (
                      <HolidayDatesFields
                        theme={theme}
                        dates={own}
                        onChange={(next) => {
                          const { [theme.key]: _previous, ...others } = dates;
                          commit(
                            enabled,
                            titles,
                            vibes,
                            next ? { ...others, [theme.key]: next } : others,
                          );
                        }}
                      />
                    ) : null}
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
        {themes.some((theme) => enabled.has(theme.key)) || hasCustom ? (
          <p className="text-muted-foreground text-[13px]">
            The row shows whichever holiday is in season and hides itself when none is.
          </p>
        ) : (
          <p role="status" className="text-destructive text-[13px]">
            Pick at least one holiday or add your own.
          </p>
        )}
      </fieldset>
      <CustomHolidays config={config} onChange={onChange} max={catalog.data.max_custom_holidays} />
      <SourceField config={config} onChange={onChange} />
    </div>
  );
}

/** The Family movie night variant: its vibe and where its picks come from. */
export function FamilyNightFields({ config, onChange }: FieldProps) {
  const catalog = useSeasonalThemes();
  if (!catalog.data) return <CatalogStatus isError={catalog.isError} />;
  const theme = catalog.data.themes.find((entry) => entry.key === FAMILY_NIGHT);
  const vibes = stringMap(config.vibes);
  return (
    <div className="grid gap-5">
      {theme && theme.vibes.length > 1 ? (
        <div className="grid gap-1.5">
          <Label>Vibe</Label>
          <VibeSelect
            theme={theme}
            value={chosenVibe(theme, vibes)}
            onChange={(key) => onChange({ ...config, vibes: { [FAMILY_NIGHT]: key } })}
          />
        </div>
      ) : null}
      <SourceField config={config} onChange={onChange} />
    </div>
  );
}
