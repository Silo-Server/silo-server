import { useMemo, useRef, useState } from "react";
import { Plus, RotateCcw, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAdminServerSettings, useUpdateServerSetting } from "@/hooks/queries/admin/settings";

import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";
import {
  DEFAULT_BUCKET_POLICIES,
  LOG_LEVEL_OPTIONS,
  OPSLOG_BUCKET_POLICIES_KEY,
  OPSLOG_MAX_ROWS_KEY,
  OPSLOG_MAX_SIZE_MB_KEY,
  OPSLOG_RETENTION_DAYS_KEY,
  parseBucketPolicies,
  serializeBucketPolicies,
  type LogRetentionBucketPolicy,
} from "./logRetentionPolicy";

type BucketRow = LogRetentionBucketPolicy & { id: string };

const GLOBAL_KEYS = [
  OPSLOG_RETENTION_DAYS_KEY,
  OPSLOG_MAX_ROWS_KEY,
  OPSLOG_MAX_SIZE_MB_KEY,
] as const;

function createBucketRow(policy?: Partial<LogRetentionBucketPolicy>, fallbackID = "0"): BucketRow {
  return {
    id: fallbackID,
    component: policy?.component ?? "",
    level: policy?.level ?? "info",
    retention_days: policy?.retention_days ?? 1,
    max_rows: policy?.max_rows ?? 100000,
    max_size_mb: policy?.max_size_mb ?? 128,
  };
}

export default function LogRetentionSettings() {
  const { data: settings, isLoading } = useAdminServerSettings();
  const updateSetting = useUpdateServerSetting();

  const [localValues, setLocalValues] = useState<Record<string, string>>({});
  const [bucketRows, setBucketRows] = useState<BucketRow[]>([]);
  const [dirty, setDirty] = useState<Set<string>>(new Set());
  const [restartRequired, setRestartRequired] = useState(false);
  const [parseError, setParseError] = useState<string>("");
  const [saveError, setSaveError] = useState("");
  const nextRowID = useRef(1);

  const globalKeys = useMemo(() => [...GLOBAL_KEYS], []);
  const hydratedState = useMemo(() => {
    const nextValues: Record<string, string> = {};
    for (const key of globalKeys) {
      nextValues[key] = settings?.[key] ?? "";
    }

    try {
      const parsed = parseBucketPolicies(settings?.[OPSLOG_BUCKET_POLICIES_KEY] ?? "");
      return {
        localValues: nextValues,
        bucketRows: parsed.map((policy, index) => createBucketRow(policy, String(index + 1))),
        parseError: "",
        nextRowID: parsed.length + 1,
      };
    } catch (error) {
      return {
        localValues: nextValues,
        bucketRows: DEFAULT_BUCKET_POLICIES.map((policy, index) =>
          createBucketRow(policy, String(index + 1)),
        ),
        parseError: error instanceof Error ? error.message : "Failed to parse bucket rules",
        nextRowID: DEFAULT_BUCKET_POLICIES.length + 1,
      };
    }
  }, [globalKeys, settings]);

  const effectiveLocalValues =
    Object.keys(localValues).length === 0 && dirty.size === 0
      ? hydratedState.localValues
      : localValues;
  const effectiveBucketRows =
    dirty.has(OPSLOG_BUCKET_POLICIES_KEY) || bucketRows.length > 0
      ? bucketRows
      : hydratedState.bucketRows;
  const effectiveParseError = dirty.has(OPSLOG_BUCKET_POLICIES_KEY)
    ? parseError
    : hydratedState.parseError;

  const dirtyCount = dirty.size;

  function getValue(key: string) {
    return effectiveLocalValues[key] ?? settings?.[key] ?? "";
  }

  function setValue(key: string, value: string) {
    setLocalValues((prev) => ({ ...prev, [key]: value }));
    setDirty((prev) => new Set(prev).add(key));
  }

  function updateBucketRow(id: string, field: keyof LogRetentionBucketPolicy, value: string) {
    setBucketRows((prev) =>
      prev.map((row) =>
        row.id === id
          ? {
              ...row,
              [field]:
                field === "component" || field === "level"
                  ? value
                  : Math.max(0, Number.parseInt(value, 10) || 0),
            }
          : row,
      ),
    );
    setDirty((prev) => new Set(prev).add(OPSLOG_BUCKET_POLICIES_KEY));
  }

  function addBucketRow() {
    const nextIdValue = Math.max(
      nextRowID.current,
      ...effectiveBucketRows.map((row) => Number.parseInt(row.id, 10) || 0),
      0,
    );
    const id = String(nextIdValue + 1);
    nextRowID.current = nextIdValue + 1;
    setBucketRows((prev) => [...prev, createBucketRow(undefined, id)]);
    setDirty((prev) => new Set(prev).add(OPSLOG_BUCKET_POLICIES_KEY));
  }

  function removeBucketRow(id: string) {
    setBucketRows((prev) => prev.filter((row) => row.id !== id));
    setDirty((prev) => new Set(prev).add(OPSLOG_BUCKET_POLICIES_KEY));
  }

  function restoreRecommendedBuckets() {
    setBucketRows(
      DEFAULT_BUCKET_POLICIES.map((policy, index) => createBucketRow(policy, String(index + 1))),
    );
    nextRowID.current = DEFAULT_BUCKET_POLICIES.length + 1;
    setDirty((prev) => new Set(prev).add(OPSLOG_BUCKET_POLICIES_KEY));
    setParseError("");
  }

  async function save() {
    setSaveError("");
    const requests = Array.from(dirty).map((key) => {
      const value =
        key === OPSLOG_BUCKET_POLICIES_KEY
          ? serializeBucketPolicies(effectiveBucketRows)
          : (effectiveLocalValues[key] ?? "");
      return updateSetting.mutateAsync({ key, value });
    });
    try {
      await Promise.all(requests);
      setDirty(new Set());
      setRestartRequired(true);
    } catch {
      setSaveError("Failed to save some settings. Please try again.");
    }
  }

  function discard() {
    if (!settings) {
      return;
    }
    const nextValues: Record<string, string> = {};
    for (const key of globalKeys) {
      nextValues[key] = settings[key] ?? "";
    }
    setLocalValues(nextValues);
    setBucketRows(hydratedState.bucketRows);
    nextRowID.current = hydratedState.nextRowID;
    setParseError(hydratedState.parseError);
    setDirty(new Set());
    setRestartRequired(false);
  }

  if (isLoading) {
    return <div>Loading...</div>;
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Log Retention</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Prune oldest operational logs by global caps and per-bucket overrides. Bucket rules match
          on component and level. Cleanup cadence and startup runs are configured in Scheduled
          Tasks.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Global Limits">
          <SettingField
            label="Retention Days"
            type="number"
            hint="Logs older than this are pruned first."
            value={getValue(OPSLOG_RETENTION_DAYS_KEY)}
            onChange={(value) => setValue(OPSLOG_RETENTION_DAYS_KEY, value)}
          />
          <SettingField
            label="Max Rows"
            type="number"
            hint="Keeps only the newest rows once this total is exceeded."
            value={getValue(OPSLOG_MAX_ROWS_KEY)}
            onChange={(value) => setValue(OPSLOG_MAX_ROWS_KEY, value)}
          />
          <SettingField
            label="Max Size (MB)"
            type="number"
            hint="Uses estimated log row size. Oldest rows are pruned when the budget is exceeded."
            value={getValue(OPSLOG_MAX_SIZE_MB_KEY)}
            onChange={(value) => setValue(OPSLOG_MAX_SIZE_MB_KEY, value)}
          />
        </FieldGroup>

        <FieldGroup label="Bucket Overrides">
          <div className="space-y-4 py-3">
            <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-center">
              <div className="text-muted-foreground text-sm">
                Use tighter rules for noisy buckets like{" "}
                <span className="font-mono">metadata/info</span>. Set a bucket limit to{" "}
                <span className="font-mono">0</span> to disable that bucket-specific cap.
              </div>
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={restoreRecommendedBuckets}
                >
                  <RotateCcw className="size-4" />
                  Restore Recommended Rules
                </Button>
                <Button type="button" size="sm" onClick={addBucketRow}>
                  <Plus className="size-4" />
                  Add Rule
                </Button>
              </div>
            </div>

            {effectiveParseError ? (
              <div className="border-warning/30 bg-warning/10 text-warning rounded-[1rem] border px-3 py-2 text-sm">
                Existing bucket policy JSON could not be parsed. The editor loaded the recommended
                rules so you can recover cleanly. Details: {effectiveParseError}
              </div>
            ) : null}

            {saveError && <p className="text-sm text-red-400">{saveError}</p>}

            <div className="surface-panel-subtle overflow-x-auto rounded-[1rem]">
              <table className="w-full border-collapse text-sm">
                <thead className="bg-muted/40 text-left">
                  <tr>
                    <th className="px-3 py-2 font-medium">Component</th>
                    <th className="px-3 py-2 font-medium">Level</th>
                    <th className="px-3 py-2 font-medium">Days</th>
                    <th className="px-3 py-2 font-medium">Max Rows</th>
                    <th className="px-3 py-2 font-medium">Max Size (MB)</th>
                    <th className="w-[60px] px-3 py-2 font-medium"> </th>
                  </tr>
                </thead>
                <tbody>
                  {effectiveBucketRows.length === 0 ? (
                    <tr>
                      <td colSpan={6} className="text-muted-foreground px-3 py-6 text-center">
                        No bucket overrides configured.
                      </td>
                    </tr>
                  ) : (
                    effectiveBucketRows.map((row) => (
                      <tr key={row.id} className="border-t">
                        <td className="px-3 py-2">
                          <Input
                            value={row.component}
                            onChange={(event) =>
                              updateBucketRow(row.id, "component", event.target.value)
                            }
                            placeholder="metadata"
                          />
                        </td>
                        <td className="px-3 py-2">
                          <Select
                            value={row.level}
                            onValueChange={(value) => updateBucketRow(row.id, "level", value)}
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
                            onChange={(event) =>
                              updateBucketRow(row.id, "retention_days", event.target.value)
                            }
                            className="w-[110px]"
                          />
                        </td>
                        <td className="px-3 py-2">
                          <Input
                            type="number"
                            min="0"
                            value={String(row.max_rows)}
                            onChange={(event) =>
                              updateBucketRow(row.id, "max_rows", event.target.value)
                            }
                            className="w-[140px]"
                          />
                        </td>
                        <td className="px-3 py-2">
                          <Input
                            type="number"
                            min="0"
                            value={String(row.max_size_mb)}
                            onChange={(event) =>
                              updateBucketRow(row.id, "max_size_mb", event.target.value)
                            }
                            className="w-[140px]"
                          />
                        </td>
                        <td className="px-3 py-2 text-right">
                          <Button
                            type="button"
                            size="icon-sm"
                            variant="outline"
                            onClick={() => removeBucketRow(row.id)}
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

            <div className="text-muted-foreground text-xs leading-5">
              Matching rows are pruned oldest-first when they exceed the bucket rule. Global caps
              still apply afterward, so noisy buckets cannot crowd out playback or error logs.
            </div>
          </div>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={dirtyCount}
        onSave={save}
        onDiscard={discard}
        isSaving={updateSetting.isPending}
        restartRequired={restartRequired}
      />
    </div>
  );
}
