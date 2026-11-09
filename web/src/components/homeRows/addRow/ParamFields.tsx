import { useId, type ReactNode } from "react";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
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
import { useT, type MessageKey } from "@/i18n";
import { sectionLibraryFilterIds, withSectionLibraryFilterIds } from "@/lib/sectionLibraryFilter";
import { hasParamFields } from "@/lib/homeRows/paramFields";
import { variantOf } from "@/lib/homeRows/variants";
import { Segmented } from "@/components/collections/fields/Segmented";
import { FamilyNightFields, HolidayFields } from "./SeasonalFields";

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
  const t = useT();
  return (
    <Field label={t("web.home.addRow.params.from")} hint={t("web.home.addRow.params.fromHint")}>
      <LibraryMultiSelect
        libraries={libraries}
        emptyLabel={t("web.home.addRow.params.allLibraries")}
        value={sectionLibraryFilterIds(config)}
        onChange={(ids) => onChange(withSectionLibraryFilterIds(config, ids))}
      />
    </Field>
  );
}

const MEDIA_TYPES: ReadonlyArray<{ value: string; label: MessageKey }> = [
  { value: "all", label: "web.home.addRow.params.mediaEverything" },
  { value: "movie", label: "web.home.addRow.params.mediaMovies" },
  { value: "series", label: "web.home.addRow.params.mediaShows" },
  { value: "audiobook", label: "web.home.addRow.params.mediaAudiobooks" },
];

// "default" keeps the list's stored order: provider sync order, newest added first.
const LIST_ORDERS: ReadonlyArray<{ value: string; label: MessageKey }> = [
  { value: "default", label: "web.home.addRow.params.orderList" },
  { value: "added_at:desc", label: "web.home.addRow.params.orderAddedDesc" },
  { value: "added_at:asc", label: "web.home.addRow.params.orderAddedAsc" },
  { value: "title:asc", label: "web.home.addRow.params.orderTitleAsc" },
  { value: "title:desc", label: "web.home.addRow.params.orderTitleDesc" },
  { value: "release_date:desc", label: "web.home.addRow.params.orderReleaseDesc" },
  { value: "release_date:asc", label: "web.home.addRow.params.orderReleaseAsc" },
  { value: "rating_imdb:desc", label: "web.home.addRow.params.orderImdbDesc" },
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
  const t = useT();
  const id = useId();
  const filterType =
    typeof config.filter_type === "string" && config.filter_type ? config.filter_type : "all";
  const libraryIds = Array.isArray(config.filter_library_ids)
    ? config.filter_library_ids.filter((value): value is number => typeof value === "number")
    : [];
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label={t("web.home.addRow.params.mediaType")} htmlFor={`${id}-type`}>
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
                {t(option.label)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field label={t("web.home.addRow.libraries")}>
        <LibraryMultiSelect
          libraries={libraries}
          emptyLabel={t("web.home.addRow.params.allLibraries")}
          value={libraryIds}
          onChange={(ids) =>
            onChange({ ...config, filter_library_ids: ids.length > 0 ? ids : undefined })
          }
        />
      </Field>
      <Field label={t("web.home.addRow.order")} htmlFor={`${id}-order`}>
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
                {t(option.label)}
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
}: FieldProps & { paramKey: string; label: string; placeholder: number; hint: string }) {
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
        placeholder={String(placeholder)}
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
  const t = useT();
  const id = useId();
  return (
    <Field
      label={t("web.home.addRow.params.genre")}
      htmlFor={id}
      hint={t("web.home.addRow.params.genreHint")}
    >
      <Input
        id={id}
        value={typeof config.genre === "string" ? config.genre : ""}
        onChange={(event) => onChange({ ...config, genre: event.target.value })}
      />
    </Field>
  );
}

const CADENCES: ReadonlyArray<{ value: string; label: MessageKey }> = [
  { value: "daily", label: "web.home.addRow.params.everyDay" },
  { value: "weekly", label: "web.home.addRow.params.everyWeek" },
  { value: "monthly", label: "web.home.addRow.params.everyMonth" },
];

function SpotlightFields({ config, onChange }: FieldProps) {
  const t = useT();
  const id = useId();
  // A preset with a pinned subject and no auto_rotate is pinned, as the
  // server reads it; defaulting the switch on would hide the subject.
  const rotating = typeof config.auto_rotate === "boolean" ? config.auto_rotate : !config.subject;
  const cadence = typeof config.rotation_cadence === "string" ? config.rotation_cadence : "weekly";
  return (
    <div className="grid gap-4">
      <div className="flex items-center justify-between gap-4">
        <Label htmlFor={`${id}-rotate`}>{t("web.home.addRow.params.autoRotate")}</Label>
        <Switch
          id={`${id}-rotate`}
          checked={rotating}
          onCheckedChange={(checked) => onChange({ ...config, auto_rotate: checked === true })}
        />
      </div>
      {rotating ? (
        <Field label={t("web.home.addRow.params.howOften")} htmlFor={`${id}-cadence`}>
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
                  {t(option.label)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      ) : (
        <Field label={t("web.home.addRow.params.subject")} htmlFor={`${id}-subject`}>
          <Input
            id={`${id}-subject`}
            placeholder={
              config.subject_type === "era"
                ? t("web.home.addRow.params.subjectEra")
                : t("web.home.addRow.params.subjectPerson")
            }
            value={typeof config.subject === "string" ? config.subject : ""}
            onChange={(event) => onChange({ ...config, subject: event.target.value })}
          />
        </Field>
      )}
    </div>
  );
}

const TRENDING_SHOWS = [
  { value: "all", label: "Both" },
  { value: "movie", label: "Movies" },
  { value: "series", label: "TV shows" },
] as const;

type TrendingShow = (typeof TRENDING_SHOWS)[number]["value"];

/** Which titles a worldwide trending row keeps; Both leaves filter_type unset. */
function TrendingShowField({ config, onChange }: FieldProps) {
  const value: TrendingShow =
    config.filter_type === "movie" || config.filter_type === "series" ? config.filter_type : "all";
  return (
    <Segmented<TrendingShow>
      label="Show"
      value={value}
      options={TRENDING_SHOWS}
      onChange={(next) => onChange({ ...config, filter_type: next === "all" ? undefined : next })}
    />
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
  const t = useT();
  if (!hasParamFields(sectionType, slot, config, onLibraryPage)) return null;
  const props = { config, onChange };
  if (slot === "primary") {
    if (sectionType === "seasonal_themed") {
      return variantOf(sectionType, config) === "se_family_movie_night" ? (
        <FamilyNightFields {...props} />
      ) : (
        <HolidayFields {...props} />
      );
    }
    if (sectionType === "trending_discover") return <TrendingShowField {...props} />;
    return <LibraryFilterField {...props} libraries={libraries} />;
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
          label={t("web.home.addRow.params.lookBack")}
          placeholder={30}
          hint={t("web.home.addRow.params.lookBackHint")}
        />
      );
    case "short_watches":
      return (
        <NumberField
          {...props}
          paramKey="max_minutes"
          label={t("web.home.addRow.params.maxMinutes")}
          placeholder={95}
          hint={t("web.home.addRow.params.maxMinutesHint")}
        />
      );
    case "anniversaries":
      return (
        <NumberField
          {...props}
          paramKey="milestone_years"
          label={t("web.home.addRow.params.milestone")}
          placeholder={5}
          hint={t("web.home.addRow.params.milestoneHint")}
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
