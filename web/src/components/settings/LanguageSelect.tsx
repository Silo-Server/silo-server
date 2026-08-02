import { useState, type ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { canonicalLanguageTag, getLanguageName } from "@/lib/languageNames";
import type { SettingOption } from "@/lib/languageOptions";

const OTHER_VALUE = "__other__";

interface LanguageSelectProps {
  /** Raw select value, including any caller sentinel ("none", inherit, …). */
  value: string;
  /**
   * Receives either a caller sentinel from {@link children}, an option value,
   * or a free-typed BCP 47 tag committed through "Other…".
   */
  onValueChange: (value: string) => void;
  options: readonly SettingOption[];
  id?: string;
  disabled?: boolean;
  placeholder?: string;
  /** Class for the select trigger. */
  className?: string;
  /**
   * Whether the "Other…" free entry is offered. Callers pass false when the
   * value is constrained to an explicit permitted list, where a typed tag
   * would only be rejected on save.
   */
  allowOther?: boolean;
  "aria-label"?: string;
  "aria-describedby"?: string;
  /** Leading sentinel items ("No preference", "Inherit", …) as SelectItems. */
  children?: ReactNode;
}

/**
 * A language picker over the contract's advisory option floor.
 *
 * The floor deliberately stays a short authored list — the server no longer
 * scans the catalog for every audio and subtitle track language (that walk
 * took tens of seconds on large deployments). Because these settings are open
 * `language_tag` values, any language beyond the floor is reachable through
 * "Other…", which accepts a BCP 47 tag and previews the resolved language
 * name before committing. A stored off-floor value stays selectable because
 * the option builders always synthesize the current value into the list.
 */
export function LanguageSelect({
  value,
  onValueChange,
  options,
  id,
  disabled = false,
  placeholder,
  className,
  allowOther = true,
  "aria-label": ariaLabel,
  "aria-describedby": ariaDescribedBy,
  children,
}: LanguageSelectProps) {
  // null = closed; otherwise the tag being typed.
  const [draft, setDraft] = useState<string | null>(null);

  const trimmed = (draft ?? "").trim();
  const draftTag = trimmed ? canonicalLanguageTag(trimmed) : null;
  const draftInvalid = trimmed.length > 0 && draftTag === null;

  const commitDraft = () => {
    if (!draftTag) return;
    onValueChange(trimmed);
    setDraft(null);
  };

  return (
    <div className="w-full min-w-0 space-y-2">
      <Select
        value={value}
        disabled={disabled}
        onValueChange={(next) => {
          if (next === OTHER_VALUE) {
            // Keep the current selection; the free entry commits separately.
            setDraft("");
            return;
          }
          setDraft(null);
          onValueChange(next);
        }}
      >
        <SelectTrigger
          id={id}
          aria-label={ariaLabel}
          aria-describedby={ariaDescribedBy}
          className={className}
          disabled={disabled}
        >
          <SelectValue placeholder={placeholder} />
        </SelectTrigger>
        <SelectContent>
          {children}
          {options.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
          {allowOther && (
            <>
              <SelectSeparator />
              <SelectItem value={OTHER_VALUE}>Other…</SelectItem>
            </>
          )}
        </SelectContent>
      </Select>

      {draft !== null && (
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <Input
              autoFocus
              value={draft}
              disabled={disabled}
              placeholder="Language code, e.g. pt-BR"
              aria-label="Language code"
              aria-invalid={draftInvalid || undefined}
              className="h-9 flex-1"
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  commitDraft();
                } else if (event.key === "Escape") {
                  event.preventDefault();
                  setDraft(null);
                }
              }}
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-9"
              disabled={disabled || !draftTag}
              onClick={commitDraft}
            >
              Use
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-9"
              disabled={disabled}
              onClick={() => setDraft(null)}
            >
              Cancel
            </Button>
          </div>
          <p
            className="text-muted-foreground text-[11px] leading-tight"
            role={draftInvalid ? "alert" : undefined}
          >
            {draftInvalid
              ? "Not a valid language tag. Use an ISO code such as is, yue, or pt-BR."
              : draftTag
                ? getLanguageName(trimmed)
                : "Type an ISO language code to use a language not in the list."}
          </p>
        </div>
      )}
    </div>
  );
}
