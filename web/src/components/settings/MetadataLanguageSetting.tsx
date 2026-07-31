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
import type { SettingOption } from "@/lib/languageOptions";
import {
  ORIGINAL_METADATA_LANGUAGE,
  withMetadataLanguageOverride,
  withoutMetadataLanguageOverride,
  type MetadataLanguageOverrides,
} from "@/lib/metadataLanguagePreferences";
import { getLanguageName } from "@/player/utils/languageNames";

interface MetadataLanguageSettingProps {
  fallback: string | null;
  overrides: MetadataLanguageOverrides;
  languageOptions: readonly SettingOption[];
  disabled?: boolean;
  onFallbackChange: (language: string | null) => void;
  onOverridesChange: (overrides: MetadataLanguageOverrides) => void;
}

const NO_PREFERENCE = "__library_default";

export function MetadataLanguageSetting({
  fallback,
  overrides,
  languageOptions,
  disabled = false,
  onFallbackChange,
  onOverridesChange,
}: MetadataLanguageSettingProps) {
  const [newSource, setNewSource] = useState("");
  const entries = useMemo(
    () =>
      Object.entries(overrides).sort(([left], [right]) =>
        getLanguageName(left).localeCompare(getLanguageName(right)),
      ),
    [overrides],
  );
  const namedOptions = useMemo(
    () => languageOptions.filter((language) => language.value !== ORIGINAL_METADATA_LANGUAGE),
    [languageOptions],
  );
  const availableSources = namedOptions.filter((language) => !(language.value in overrides));
  const optionsForTarget = (target: string) => {
    if (
      target === ORIGINAL_METADATA_LANGUAGE ||
      namedOptions.some((language) => language.value === target)
    ) {
      return namedOptions;
    }
    return [{ value: target, label: getLanguageName(target) }, ...namedOptions];
  };

  const addException = () => {
    if (!newSource) return;
    onOverridesChange(
      withMetadataLanguageOverride(overrides, newSource, ORIGINAL_METADATA_LANGUAGE),
    );
    setNewSource("");
  };

  return (
    <SettingRow
      label="Metadata language"
      description="Choose the fallback for titles and descriptions, then add exceptions based on each item's original language. Missing descriptions can be translated automatically when AI translation is enabled."
      control={(id) => (
        <div className="w-full space-y-3 md:w-[430px]">
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
                  className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.35fr)_auto] items-center gap-2 py-2"
                >
                  <span className="truncate text-sm">{getLanguageName(source)}</span>
                  <Select
                    value={target}
                    onValueChange={(value) =>
                      onOverridesChange(withMetadataLanguageOverride(overrides, source, value))
                    }
                  >
                    <SelectTrigger
                      aria-label={`Metadata language for ${getLanguageName(source)}`}
                      className="h-8 w-full"
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
                    className="size-8"
                    disabled={disabled}
                    aria-label={`Remove ${getLanguageName(source)} exception`}
                    onClick={() =>
                      onOverridesChange(withoutMetadataLanguageOverride(overrides, source))
                    }
                  >
                    <Trash2 className="size-3.5" aria-hidden="true" />
                  </Button>
                </div>
              ))}
            </div>
          ) : null}

          {availableSources.length > 0 ? (
            <div className="flex items-center gap-2">
              <Select value={newSource} onValueChange={setNewSource}>
                <SelectTrigger
                  aria-label="Original language for new exception"
                  className="h-9 min-w-0 flex-1"
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
