import { useMemo, useState } from "react";
import { Plus, Trash2 } from "lucide-react";

import { SettingRow } from "@/components/settings/SettingRow";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { withCurrentLanguageOption, type SettingOption } from "@/lib/languageOptions";
import { getLanguageName } from "@/lib/languageNames";
import {
  ORIGINAL_METADATA_LANGUAGE,
  withMetadataLanguageOverride,
  withoutMetadataLanguageOverride,
  type MetadataLanguageOverrides,
} from "@/lib/metadataLanguagePreferences";

interface MetadataLanguageSettingProps {
  fallback: string | null;
  overrides: MetadataLanguageOverrides;
  languageOptions: readonly SettingOption[];
  disabled?: boolean;
  onFallbackChange: (language: string | null) => void;
  onOverridesChange: (overrides: MetadataLanguageOverrides) => void | Promise<void>;
}

const NO_PREFERENCE = "__library_default";

function overridesEqual(
  left: MetadataLanguageOverrides,
  right: MetadataLanguageOverrides,
): boolean {
  const leftEntries = Object.entries(left);
  return (
    leftEntries.length === Object.keys(right).length &&
    leftEntries.every(([source, target]) => right[source] === target)
  );
}

export function MetadataLanguageSetting({
  fallback,
  overrides,
  languageOptions,
  disabled = false,
  onFallbackChange,
  onOverridesChange,
}: MetadataLanguageSettingProps) {
  const [newSource, setNewSource] = useState("");
  const [optimisticOverrides, setOptimisticOverrides] = useState<{
    base: MetadataLanguageOverrides;
    value: MetadataLanguageOverrides;
  } | null>(null);
  const currentOverrides =
    optimisticOverrides !== null && overridesEqual(overrides, optimisticOverrides.base)
      ? optimisticOverrides.value
      : overrides;

  const entries = useMemo(
    () =>
      Object.entries(currentOverrides).sort(([left], [right]) =>
        getLanguageName(left).localeCompare(getLanguageName(right)),
      ),
    [currentOverrides],
  );
  const namedOptions = useMemo(
    () => languageOptions.filter((language) => language.value !== ORIGINAL_METADATA_LANGUAGE),
    [languageOptions],
  );
  const availableSources = namedOptions.filter((language) => !(language.value in currentOverrides));
  const optionsForTarget = (target: string) => {
    if (target === ORIGINAL_METADATA_LANGUAGE) return namedOptions;
    return withCurrentLanguageOption(namedOptions, target);
  };

  const changeOverrides = async (next: MetadataLanguageOverrides) => {
    const pending = { base: overrides, value: next };
    setOptimisticOverrides(pending);
    try {
      await onOverridesChange(next);
    } catch {
      setOptimisticOverrides((current) => (current === pending ? null : current));
    }
  };

  const addException = () => {
    if (!newSource) return;
    void changeOverrides(
      withMetadataLanguageOverride(currentOverrides, newSource, ORIGINAL_METADATA_LANGUAGE),
    );
    setNewSource("");
  };

  return (
    <SettingRow
      label="Metadata language"
      description="Choose the fallback for titles and descriptions, then add exceptions based on each item's original language. Missing descriptions can be translated automatically when AI translation is enabled."
      control={(id) => (
        <div className="w-full min-w-0 space-y-3 md:w-[430px]">
          <Select
            value={fallback ?? NO_PREFERENCE}
            onValueChange={(value) => onFallbackChange(value === NO_PREFERENCE ? null : value)}
          >
            <SelectTrigger id={id} className="w-full" disabled={disabled}>
              <SelectValue placeholder="Library default" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NO_PREFERENCE}>Library default</SelectItem>
              <SelectItem value={ORIGINAL_METADATA_LANGUAGE}>Original language</SelectItem>
              {namedOptions.map((language) => (
                <SelectItem key={language.value} value={language.value}>
                  {language.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          {entries.length > 0 ? (
            <div className="border-border/60 divide-border/60 divide-y border-y">
              {entries.map(([source, target]) => (
                <div
                  key={source}
                  className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 py-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.35fr)_auto]"
                >
                  <span className="truncate text-sm">{getLanguageName(source)}</span>
                  <Select
                    value={target}
                    onValueChange={(value) =>
                      void changeOverrides(
                        withMetadataLanguageOverride(currentOverrides, source, value),
                      )
                    }
                  >
                    <SelectTrigger
                      aria-label={`Metadata language for ${getLanguageName(source)}`}
                      className="col-span-2 col-start-1 row-start-2 h-9 w-full sm:col-span-1 sm:col-start-2 sm:row-start-1 sm:h-8"
                      disabled={disabled}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ORIGINAL_METADATA_LANGUAGE}>Original language</SelectItem>
                      {optionsForTarget(target).map((language) => (
                        <SelectItem key={language.value} value={language.value}>
                          {language.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="col-start-2 row-start-1 size-9 sm:col-start-3 sm:size-8"
                    disabled={disabled}
                    aria-label={`Remove ${getLanguageName(source)} exception`}
                    onClick={() =>
                      void changeOverrides(
                        withoutMetadataLanguageOverride(currentOverrides, source),
                      )
                    }
                  >
                    <Trash2 className="size-3.5" aria-hidden="true" />
                  </Button>
                </div>
              ))}
            </div>
          ) : null}

          {availableSources.length > 0 ? (
            <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-center">
              <Select value={newSource} onValueChange={setNewSource}>
                <SelectTrigger
                  aria-label="Original language for new exception"
                  className="h-10 w-full min-w-0 flex-1 sm:h-9 sm:w-auto"
                  disabled={disabled}
                >
                  <SelectValue placeholder="Choose original language" />
                </SelectTrigger>
                <SelectContent>
                  {availableSources.map((language) => (
                    <SelectItem key={language.value} value={language.value}>
                      {language.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-10 w-full sm:h-8 sm:w-auto"
                disabled={disabled || !newSource}
                onClick={addException}
              >
                <Plus className="mr-1.5 size-3.5" aria-hidden="true" />
                Add exception
              </Button>
            </div>
          ) : null}
        </div>
      )}
    />
  );
}
