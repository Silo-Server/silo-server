import { useId, useState, type ReactNode } from "react";
import { Check, ChevronRight, X } from "lucide-react";

import type { PluginAdminFormField, RequestIntegration } from "@/api/types";
import { coerceFieldValue, parseFieldTypes } from "@/components/admin/plugins/schemaFormUtils";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useRequestIntegrationOptions } from "@/hooks/queries/admin/requests";
import { cn } from "@/lib/utils";

import {
  overrideLabel,
  serverOverrideFields,
  splitOverrideFields,
  type DestinationChoice,
  type Tier,
} from "./requestRoutingModel";
import {
  serverConfigSchema,
  serverFitsTier,
  serverInstallation,
  serverIs4K,
  type RequestRouterInstallation,
} from "./requestServerModel";

/** Select values for the choices that are not a server. */
const DEST_PASS = "__pass__";
const DEST_SKIP = "__skip__";
const SERVER_SETTING = "__server__";

export interface Choice {
  value: string;
  label: string;
}

/** An inline error under a field, announced when it appears. */
export function FieldError({ children }: { children?: string }) {
  if (!children) return null;
  return (
    <p role="alert" className="text-destructive text-xs">
      {children}
    </p>
  );
}

/**
 * A row of toggle chips for a short, fixed list (a server's tags): every
 * choice visible, each one a pressed or unpressed button.
 */
function ChipToggleList({
  label,
  options,
  selected,
  onChange,
  className,
}: {
  label: string;
  options: readonly Choice[];
  selected: readonly string[];
  onChange: (next: string[]) => void;
  className?: string;
}) {
  return (
    <div role="group" aria-label={label} className={cn("flex flex-wrap gap-1.5", className)}>
      {options.map((option) => {
        const on = selected.includes(option.value);
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={on}
            onClick={() =>
              onChange(
                on
                  ? selected.filter((value) => value !== option.value)
                  : [...selected, option.value],
              )
            }
            className={cn(
              "focus-visible:ring-ring inline-flex h-7 items-center gap-1 rounded-full border px-2.5 text-xs transition-colors focus-visible:ring-2 focus-visible:outline-none",
              on
                ? "border-primary bg-primary text-primary-foreground"
                : "border-muted-foreground/25 bg-background text-muted-foreground hover:text-foreground",
            )}
          >
            {on ? <Check className="size-3" aria-hidden="true" /> : null}
            {option.label}
          </button>
        );
      })}
    </div>
  );
}

/**
 * A field in a destination panel: its label above the control, an optional
 * hint and error under it. Dialogs are too narrow for the settings page's
 * side-by-side rows.
 */
function StackedField({
  label,
  htmlFor,
  labelId,
  hint,
  error,
  className,
  children,
}: {
  label: string;
  htmlFor?: string;
  labelId?: string;
  hint?: ReactNode;
  error?: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("min-w-0 space-y-1.5", className)}>
      <label
        id={labelId}
        htmlFor={htmlFor}
        className="text-muted-foreground block text-xs font-medium"
      >
        {label}
      </label>
      {children}
      {hint ? <p className="text-muted-foreground text-xs">{hint}</p> : null}
      <FieldError>{error}</FieldError>
    </div>
  );
}

/**
 * The chosen values of a list (genres, languages, accounts) as removable
 * chips, and one select to add another.
 */
export function ValuePicker({
  addLabel,
  options,
  selected,
  onChange,
  labelOf,
  disabled,
  unavailableHint,
  hideAdd = false,
}: {
  addLabel: string;
  options: readonly Choice[];
  selected: readonly string[];
  onChange: (next: string[]) => void;
  labelOf?: (value: string) => string;
  disabled?: boolean;
  /** Why there is nothing to pick from; shown in place of the select. Chosen values stay. */
  unavailableHint?: string;
  /** Shows only the chips; the caller offers its own way to add. */
  hideAdd?: boolean;
}) {
  const nameOf = (value: string) =>
    labelOf?.(value) ?? options.find((option) => option.value === value)?.label ?? value;
  const available = options.filter((option) => !selected.includes(option.value));
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
      {selected.length > 0 ? (
        <ul className="contents list-none">
          {selected.map((value) => (
            <li
              key={value}
              className="border-border bg-accent/60 inline-flex items-center gap-1 rounded-md border py-0.5 pr-0.5 pl-2 text-xs"
            >
              {nameOf(value)}
              <Button
                type="button"
                size="icon-xs"
                variant="ghost"
                aria-label={`Remove ${nameOf(value)}`}
                onClick={() => onChange(selected.filter((entry) => entry !== value))}
              >
                <X />
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      {hideAdd ? null : unavailableHint ? (
        <p className="text-muted-foreground text-xs">{unavailableHint}</p>
      ) : (
        <Select
          value=""
          onValueChange={(value) => {
            if (value) onChange([...selected, value]);
          }}
          disabled={disabled || available.length === 0}
        >
          <SelectTrigger aria-label={addLabel} size="sm" className="w-auto min-w-32 text-xs">
            <SelectValue placeholder={addLabel} />
          </SelectTrigger>
          <SelectContent>
            {available.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    </div>
  );
}

function overrideValue(value: unknown): string {
  return value === undefined || value === null ? "" : String(value);
}

/**
 * One server setting a route may replace. Unset means the server's own
 * setting applies, and the first choice says what that is.
 */
function OverrideField({
  field,
  label,
  server,
  options,
  optionsLoading,
  value,
  onChange,
  error,
}: {
  field: PluginAdminFormField;
  label: string;
  server: RequestIntegration;
  options: readonly Choice[];
  optionsLoading: boolean;
  value: unknown;
  onChange: (raw: unknown) => void;
  error?: string;
}) {
  const controlId = useId();
  const serverValue = server.plugin_config?.[field.key] ?? field.default_value;

  if (field.control === "MULTI_SELECT") {
    const selected = Array.isArray(value) ? value.map((entry) => String(entry)) : [];
    return (
      <StackedField
        label={label}
        className="sm:col-span-2"
        hint={
          selected.length === 0 && options.length > 0
            ? `None picked, so ${server.name}'s own ${label.toLowerCase()} apply.`
            : undefined
        }
        error={error}
      >
        {optionsLoading && options.length === 0 ? (
          <p className="text-muted-foreground text-xs">Loading…</p>
        ) : options.length === 0 ? (
          <p className="text-muted-foreground text-xs">Nothing to choose on this server.</p>
        ) : (
          <ChipToggleList label={label} options={options} selected={selected} onChange={onChange} />
        )}
      </StackedField>
    );
  }

  const choices: Choice[] =
    field.control === "SWITCH"
      ? [
          { value: "true", label: "On" },
          { value: "false", label: "Off" },
        ]
      : [...options];
  const current = overrideValue(value);
  if (current && !choices.some((choice) => choice.value === current)) {
    choices.push({ value: current, label: current });
  }
  const serverLabel =
    field.control === "SWITCH"
      ? serverValue === true || serverValue === "true"
        ? "On"
        : "Off"
      : (choices.find((choice) => choice.value === overrideValue(serverValue))?.label ??
        overrideValue(serverValue));

  // Folder paths (with their free space) are long; they get a whole row.
  const wide = field.key.includes("folder");
  return (
    <StackedField
      label={label}
      htmlFor={controlId}
      error={error}
      className={wide ? "sm:col-span-2" : undefined}
    >
      <Select
        value={current || SERVER_SETTING}
        onValueChange={(next) => onChange(next === SERVER_SETTING ? undefined : next)}
      >
        <SelectTrigger id={controlId} className="w-full min-w-0" aria-invalid={Boolean(error)}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={SERVER_SETTING}>
            {serverLabel ? `Server default (${serverLabel})` : "Server default"}
          </SelectItem>
          {choices.map((choice) => (
            <SelectItem key={choice.value} value={choice.value}>
              {choice.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </StackedField>
  );
}

/**
 * The settings a destination replaces on its server: Folder, Quality and
 * Tags on their own rows, and every other field the plugin offers under
 * More settings, which opens when one of them is set.
 */
function DestinationOverrides({
  sectionId,
  server,
  installations,
  overrides,
  onChange,
  errors,
  errorPrefix,
}: {
  sectionId: string;
  server: RequestIntegration;
  installations: RequestRouterInstallation[];
  overrides: Record<string, unknown>;
  onChange: (overrides: Record<string, unknown>) => void;
  errors: Record<string, string>;
  errorPrefix: string;
}) {
  const entry = serverInstallation(installations, server.installation_id, server.capability_id);
  const fieldTypes = parseFieldTypes(serverConfigSchema(entry).jsonSchema);
  const fields = serverOverrideFields(server, installations);
  const { inline, more } = splitOverrideFields(fields);
  const needsOptions = fields.some((field) => field.dynamic_options);
  const options = useRequestIntegrationOptions(needsOptions ? server.id : undefined);

  if (fields.length === 0) return null;

  function set(field: PluginAdminFormField, raw: unknown) {
    const next = { ...overrides };
    if (raw === undefined || (Array.isArray(raw) && raw.length === 0)) {
      delete next[field.key];
    } else {
      next[field.key] = coerceFieldValue(field, raw, fieldTypes[field.key]);
    }
    onChange(next);
  }

  const row = (field: PluginAdminFormField, label: string) => (
    <OverrideField
      key={field.key}
      field={field}
      label={label}
      server={server}
      options={field.dynamic_options ? (options.data?.[field.key] ?? []) : (field.options ?? [])}
      optionsLoading={Boolean(field.dynamic_options) && options.isLoading}
      value={overrides[field.key]}
      onChange={(raw) => set(field, raw)}
      error={errors[`${errorPrefix}.overrides.${field.key}`]}
    />
  );
  const moreSet = more.filter((field) => overrides[field.key] !== undefined).length;
  const moreErrors = more.some((field) => errors[`${errorPrefix}.overrides.${field.key}`]);

  return (
    <>
      {options.isError ? (
        <p className="text-xs text-amber-600 dark:text-amber-400">
          Couldn&apos;t read folders and profiles from {server.name}:{" "}
          {options.error instanceof Error ? options.error.message : "unknown error"}
        </p>
      ) : null}
      <div className="grid gap-3 sm:grid-cols-2">
        {inline.map((field) => row(field, overrideLabel(field.key)))}
      </div>
      {more.length > 0 ? (
        <MoreSettings
          id={`requests.route-more.${sectionId}`}
          changed={moreSet}
          forceOpen={moreErrors}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            {more.map((field) => row(field, field.label || overrideLabel(field.key)))}
          </div>
        </MoreSettings>
      ) : null}
    </>
  );
}

/**
 * The server settings a destination rarely changes, folded away until asked
 * for, or while one is set or has an error.
 */
function MoreSettings({
  id,
  changed,
  forceOpen,
  children,
}: {
  id: string;
  changed: number;
  forceOpen: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(changed > 0);
  // Settings that arrive set (a conflict reload, a preset) open the panel, so
  // their values are not saved unseen.
  const [prevChanged, setPrevChanged] = useState(changed);
  if (changed !== prevChanged) {
    setPrevChanged(changed);
    if (prevChanged === 0 && changed > 0) setOpen(true);
  }
  const shown = open || forceOpen;
  const panelId = useId();
  return (
    <div className="border-border/60 border-t pt-3">
      <button
        type="button"
        aria-expanded={shown}
        aria-controls={panelId}
        data-section={id}
        onClick={() => setOpen(!shown)}
        className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs font-medium"
      >
        <ChevronRight
          className={cn("size-3.5 transition-transform", shown && "rotate-90")}
          aria-hidden="true"
        />
        More settings
        {changed > 0 ? <span className="text-foreground/80">· {changed} changed</span> : null}
      </button>
      {shown ? (
        <div id={panelId} className="pt-3">
          {children}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Where one copy (HD or 4K) goes: a server, "Don't make a 4K copy", or the
 * pass-through choice, and under a chosen server the settings the route
 * replaces on it. `errors` holds the editor's field errors keyed like the
 * API's (`hd.integration_id`, `uhd.overrides.root_folder`, ...).
 */
export function RouteDestinationEditor({
  tier,
  sectionId,
  servers,
  allServers,
  installations,
  value,
  onChange,
  passLabel,
  allowSkip = false,
  caption,
  note,
  errors,
}: {
  tier: Tier;
  /**
   * Names this destination's More settings, e.g. `fallback-movie.hd`, so each
   * keeps its own open state.
   */
  sectionId: string;
  /**
   * The media type's servers. Only those that fit the tier are offered: the
   * ones marked 4K for the 4K version, the others for HD.
   */
  servers: readonly RequestIntegration[];
  /** Every server, to name one that no longer fits the media type. */
  allServers: readonly RequestIntegration[];
  installations: RequestRouterInstallation[];
  value: DestinationChoice;
  onChange: (next: DestinationChoice) => void;
  /** Offers passing the copy on, labelled so; without it a server is required. */
  passLabel?: string;
  /** Offers "Don't send a 4K version". */
  allowSkip?: boolean;
  caption?: ReactNode;
  /** A line under the chosen server, e.g. what a preset set. */
  note?: ReactNode;
  errors: Record<string, string>;
}) {
  const controlId = useId();
  const { dest, skip } = value;
  const server = dest.integration_id
    ? allServers.find((candidate) => candidate.id === dest.integration_id)
    : undefined;
  const serverChoices: Choice[] = servers
    .filter((candidate) => serverFitsTier(candidate, tier))
    .map((candidate) => ({
      value: candidate.id,
      label: candidate.enabled ? candidate.name : `${candidate.name} (turned off)`,
    }));
  if (
    dest.integration_id &&
    !serverChoices.some((choice) => choice.value === dest.integration_id)
  ) {
    // A saved choice that no longer fits stays visible, saying why, until
    // the admin picks another.
    const why = !server
      ? ""
      : !servers.some((candidate) => candidate.id === server.id)
        ? " (wrong type)"
        : serverIs4K(server)
          ? " (marked 4K)"
          : " (not marked 4K)";
    serverChoices.push({
      value: dest.integration_id,
      label: server ? `${server.name}${why}` : "Missing server",
    });
  }
  const no4KServers =
    tier === "uhd" && !servers.some((candidate) => serverFitsTier(candidate, "uhd"));
  const selected = skip ? DEST_SKIP : dest.integration_id || (passLabel ? DEST_PASS : "");
  const error = errors[tier] ?? errors[`${tier}.integration_id`];
  const label = tier === "hd" ? "HD version" : "4K version";

  function select(next: string) {
    if (next === DEST_SKIP) onChange({ dest: { integration_id: "", overrides: {} }, skip: true });
    else if (next === DEST_PASS)
      onChange({ dest: { integration_id: "", overrides: {} }, skip: false });
    else if (next === dest.integration_id) onChange({ dest, skip: false });
    else onChange({ dest: { integration_id: next, overrides: {} }, skip: false });
  }

  const headingId = useId();
  const captionId = useId();
  const sendToId = useId();
  return (
    <section
      aria-labelledby={headingId}
      className="border-border/70 bg-foreground/[0.02] space-y-3 rounded-xl border p-4"
    >
      <div className="space-y-0.5">
        <h4 id={headingId} className="text-sm font-medium">
          {label}
        </h4>
        {caption ? (
          <p id={captionId} className="text-muted-foreground text-xs">
            {caption}
          </p>
        ) : null}
      </div>
      <StackedField label="Send to" htmlFor={controlId} labelId={sendToId} error={error}>
        <Select value={selected} onValueChange={select}>
          <SelectTrigger
            id={controlId}
            aria-labelledby={`${headingId} ${sendToId}`}
            aria-describedby={caption ? captionId : undefined}
            className="w-full min-w-0 sm:w-1/2"
            aria-invalid={Boolean(error)}
          >
            <SelectValue placeholder="Choose a server" />
          </SelectTrigger>
          <SelectContent>
            {serverChoices.map((choice) => (
              <SelectItem key={choice.value} value={choice.value}>
                {choice.label}
              </SelectItem>
            ))}
            {allowSkip ? (
              <SelectItem value={DEST_SKIP}>Don&apos;t send a 4K version</SelectItem>
            ) : null}
            {passLabel ? <SelectItem value={DEST_PASS}>{passLabel}</SelectItem> : null}
          </SelectContent>
        </Select>
      </StackedField>
      {no4KServers ? (
        <p className="text-muted-foreground text-xs">
          No server is marked 4K. Turn on &ldquo;4K server&rdquo; on a server to send 4K versions to
          it.
        </p>
      ) : null}
      {server ? (
        <DestinationOverrides
          key={server.id}
          sectionId={sectionId}
          server={server}
          installations={installations}
          overrides={dest.overrides}
          onChange={(overrides) => onChange({ dest: { ...dest, overrides }, skip: false })}
          errors={errors}
          errorPrefix={tier}
        />
      ) : null}
      {server && note ? <p className="text-muted-foreground text-xs">{note}</p> : null}
    </section>
  );
}
