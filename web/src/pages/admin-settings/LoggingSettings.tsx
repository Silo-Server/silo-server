import { useMemo, useState } from "react";
import { Plus, RotateCcw, Trash2 } from "lucide-react";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SaveBar } from "@/components/SaveBar";

import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";
import {
  LOG_LEVEL_OPTIONS,
  OPSLOG_BUCKET_POLICIES_KEY,
  OPSLOG_MAX_ROWS_KEY,
  OPSLOG_MAX_SIZE_MB_KEY,
  OPSLOG_RETENTION_DAYS_KEY,
  appendBucketRow,
  bucketRowsFromRaw,
  recommendedBucketRows,
  removeBucketRow,
  serializeBucketRows,
  updateBucketRow,
  type LogRetentionBucketPolicy,
  type LogRetentionBucketRow,
} from "./logRetentionPolicy";

const SERVER_LOG_ADVANCED_KEYS = ["server.log_quiet"];
const SERVER_LOG_KEYS = ["server.log_level", ...SERVER_LOG_ADVANCED_KEYS];

// How many days of log to keep is what an admin comes here to change. The row
// and size caps are backstops whose defaults suit almost every server, and the
// policy decision log and the per-area rules are debugging tools; all of them
// sit behind Advanced.
const RETENTION_ESSENTIAL_KEYS = [OPSLOG_RETENTION_DAYS_KEY];

const RETENTION_ADVANCED_KEYS = [
  OPSLOG_MAX_ROWS_KEY,
  OPSLOG_MAX_SIZE_MB_KEY,
  "policy.decision_log_retention_days",
  "policy.decision_log_verbosity",
  "policy.decision_log_scope_sample_rate",
  OPSLOG_BUCKET_POLICIES_KEY,
];

const RETENTION_KEYS = [...RETENTION_ESSENTIAL_KEYS, ...RETENTION_ADVANCED_KEYS];

const KEYS = [...SERVER_LOG_KEYS, ...RETENTION_KEYS];

function BucketOverridesEditor({
  rows,
  parseError,
  onChange,
  onRestore,
}: {
  rows: LogRetentionBucketRow[];
  parseError: string;
  onChange: (rows: LogRetentionBucketRow[]) => void;
  onRestore: () => void;
}) {
  function edit(id: string, field: keyof LogRetentionBucketPolicy, value: string) {
    onChange(updateBucketRow(rows, id, field, value));
  }

  return (
    <div className="space-y-4 py-3.5">
      <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
        <div className="min-w-0">
          <h4 className="text-sm font-medium">Per-area limits</h4>
          <p className="text-muted-foreground mt-1 text-xs">A limit of 0 turns that rule off.</p>
        </div>
        <div className="flex shrink-0 flex-col gap-2 sm:flex-row sm:items-center">
          <Button type="button" size="sm" variant="outline" onClick={onRestore}>
            <RotateCcw className="size-4" />
            Restore Recommended Rules
          </Button>
          <Button type="button" size="sm" onClick={() => onChange(appendBucketRow(rows))}>
            <Plus className="size-4" />
            Add Rule
          </Button>
        </div>
      </div>

      {parseError ? (
        <div className="border-warning/30 bg-warning/10 text-warning rounded-[1rem] border px-3 py-2 text-sm">
          The saved rules could not be read. The editor loaded the recommended rules so you can
          recover cleanly. Details: {parseError}
        </div>
      ) : null}

      <div className="border-border/70 overflow-x-auto rounded-[1rem] border">
        <table className="w-full border-collapse text-sm">
          <thead className="bg-muted/40 text-left">
            <tr>
              <th className="px-3 py-2 font-medium">Component</th>
              <th className="px-3 py-2 font-medium">Level</th>
              <th className="px-3 py-2 font-medium">Days</th>
              <th className="px-3 py-2 font-medium">Max rows</th>
              <th className="px-3 py-2 font-medium">Max size (MB)</th>
              <th className="w-[60px] px-3 py-2 font-medium"> </th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={6} className="text-muted-foreground px-3 py-6 text-center">
                  No per-area rules configured.
                </td>
              </tr>
            ) : (
              rows.map((row) => (
                <tr key={row.id} className="border-t">
                  <td className="px-3 py-2">
                    <Input
                      value={row.component}
                      onChange={(event) => edit(row.id, "component", event.target.value)}
                      placeholder="metadata"
                      aria-label={`Component for rule ${row.id}`}
                    />
                  </td>
                  <td className="px-3 py-2">
                    <Select
                      value={row.level}
                      onValueChange={(value) => edit(row.id, "level", value)}
                    >
                      <SelectTrigger className="w-[120px]">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {LOG_LEVEL_OPTIONS.map((level) => (
                          <SelectItem key={level} value={level}>
                            {level}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </td>
                  <td className="px-3 py-2">
                    <Input
                      type="number"
                      min="0"
                      value={String(row.retention_days)}
                      onChange={(event) => edit(row.id, "retention_days", event.target.value)}
                      className="w-[110px]"
                      aria-label={`Days for rule ${row.id}`}
                    />
                  </td>
                  <td className="px-3 py-2">
                    <Input
                      type="number"
                      min="0"
                      value={String(row.max_rows)}
                      onChange={(event) => edit(row.id, "max_rows", event.target.value)}
                      className="w-[140px]"
                      aria-label={`Max rows for rule ${row.id}`}
                    />
                  </td>
                  <td className="px-3 py-2">
                    <Input
                      type="number"
                      min="0"
                      value={String(row.max_size_mb)}
                      onChange={(event) => edit(row.id, "max_size_mb", event.target.value)}
                      className="w-[140px]"
                      aria-label={`Max size for rule ${row.id}`}
                    />
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Button
                      type="button"
                      size="icon-sm"
                      variant="outline"
                      onClick={() => onChange(removeBucketRow(rows, row.id))}
                      aria-label={`Remove ${row.component || "bucket"} rule`}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

/**
 * Settings → Logging: what the server writes to its log, and how much log
 * history the database keeps. Every field is a plain batched setting, so the
 * page saves through the ordinary save bar; the per-area rules are the one JSON
 * setting, edited as a table.
 */
export default function LoggingSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();

  // The bucket rules are one JSON setting edited as a table, so the rows live
  // here while they are dirty and re-hydrate from the saved value otherwise.
  const [draftRows, setDraftRows] = useState<LogRetentionBucketRow[] | null>(null);
  const raw = form.getValue(OPSLOG_BUCKET_POLICIES_KEY);
  const bucketDirty = form.isDirty(OPSLOG_BUCKET_POLICIES_KEY);
  const hydrated = useMemo(() => bucketRowsFromRaw(raw), [raw]);
  // A stale draft can never be shown: it is only reachable while the key is
  // dirty, and the only thing that marks it dirty also sets the draft.
  const rows = bucketDirty && draftRows ? draftRows : hydrated.rows;
  const parseError = bucketDirty ? "" : hydrated.error;

  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));

  function commitRows(next: LogRetentionBucketRow[]) {
    setDraftRows(next);
    form.setValue(OPSLOG_BUCKET_POLICIES_KEY, serializeBucketRows(next));
  }

  if (form.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">Loading settings</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Logging" />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label="Server log"
          restartAll={allRestart(SERVER_LOG_KEYS)}
          dirty={anyDirty(SERVER_LOG_KEYS)}
        >
          <SettingField
            label="Log level"
            settingKey="server.log_level"
            dirty={form.isDirty("server.log_level")}
            type="select"
            description="Sets the container log's level; Debug is loud, so keep it for chasing a problem. The Server Log page records Info and above either way."
            value={form.getValue("server.log_level")}
            onChange={(v) => form.setValue("server.log_level", v)}
            restartRequired={restartKeys.has("server.log_level")}
            options={[
              { value: "debug", label: "Debug" },
              { value: "info", label: "Info" },
              { value: "warn", label: "Warn" },
              { value: "error", label: "Error" },
            ]}
          />
          <AdvancedSection
            id="logging.server"
            count={SERVER_LOG_ADVANCED_KEYS.length}
            forceOpen={anyDirty(SERVER_LOG_ADVANCED_KEYS)}
          >
            <SettingField
              label="Quiet log prefixes"
              settingKey="server.log_quiet"
              dirty={form.isDirty("server.log_quiet")}
              hint="metadata, scanner"
              description="Drops log lines starting with any of these words."
              value={form.getValue("server.log_quiet")}
              onChange={(v) => form.setValue("server.log_quiet", v)}
              restartRequired={restartKeys.has("server.log_quiet")}
            />
          </AdvancedSection>
        </FieldGroup>

        <FieldGroup
          label="Log retention"
          description="How much log history the database keeps."
          restartAll={allRestart(RETENTION_KEYS)}
          dirty={anyDirty(RETENTION_KEYS)}
        >
          <SettingField
            label="Delete log entries older than"
            settingKey={OPSLOG_RETENTION_DAYS_KEY}
            dirty={form.isDirty(OPSLOG_RETENTION_DAYS_KEY)}
            type="number"
            unit="days"
            value={form.getValue(OPSLOG_RETENTION_DAYS_KEY)}
            onChange={(v) => form.setValue(OPSLOG_RETENTION_DAYS_KEY, v)}
            restartRequired={restartKeys.has(OPSLOG_RETENTION_DAYS_KEY)}
          />

          <AdvancedSection
            id="logging.retention"
            count={RETENTION_ADVANCED_KEYS.length}
            forceOpen={anyDirty(RETENTION_ADVANCED_KEYS)}
          >
            <SettingField
              label="Maximum log entries"
              settingKey={OPSLOG_MAX_ROWS_KEY}
              dirty={form.isDirty(OPSLOG_MAX_ROWS_KEY)}
              type="number"
              value={form.getValue(OPSLOG_MAX_ROWS_KEY)}
              onChange={(v) => form.setValue(OPSLOG_MAX_ROWS_KEY, v)}
              restartRequired={restartKeys.has(OPSLOG_MAX_ROWS_KEY)}
            />
            <SettingField
              label="Maximum log size"
              settingKey={OPSLOG_MAX_SIZE_MB_KEY}
              dirty={form.isDirty(OPSLOG_MAX_SIZE_MB_KEY)}
              type="number"
              unit="MB"
              value={form.getValue(OPSLOG_MAX_SIZE_MB_KEY)}
              onChange={(v) => form.setValue(OPSLOG_MAX_SIZE_MB_KEY, v)}
              restartRequired={restartKeys.has(OPSLOG_MAX_SIZE_MB_KEY)}
            />

            <SettingsSubheading>Permission checks</SettingsSubheading>
            <SettingField
              label="Delete permission records older than"
              settingKey="policy.decision_log_retention_days"
              dirty={form.isDirty("policy.decision_log_retention_days")}
              type="number"
              unit="days"
              value={form.getValue("policy.decision_log_retention_days")}
              onChange={(v) => form.setValue("policy.decision_log_retention_days", v)}
              restartRequired={restartKeys.has("policy.decision_log_retention_days")}
            />
            <SettingField
              label="How much to record"
              settingKey="policy.decision_log_verbosity"
              dirty={form.isDirty("policy.decision_log_verbosity")}
              type="select"
              description="Full also stores a sample of each request."
              value={form.getValue("policy.decision_log_verbosity") || "digest"}
              onChange={(v) => form.setValue("policy.decision_log_verbosity", v)}
              options={[
                { value: "digest", label: "Summary" },
                { value: "verbose", label: "Full" },
              ]}
              restartRequired={restartKeys.has("policy.decision_log_verbosity")}
            />
            <SettingField
              label="Record one allowed check in every"
              settingKey="policy.decision_log_scope_sample_rate"
              dirty={form.isDirty("policy.decision_log_scope_sample_rate")}
              type="number"
              description="Denials and errors are always recorded."
              value={form.getValue("policy.decision_log_scope_sample_rate")}
              onChange={(v) => form.setValue("policy.decision_log_scope_sample_rate", v)}
              restartRequired={restartKeys.has("policy.decision_log_scope_sample_rate")}
            />

            <BucketOverridesEditor
              rows={rows}
              parseError={parseError}
              onChange={commitRows}
              onRestore={() => commitRows(recommendedBucketRows())}
            />
          </AdvancedSection>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
