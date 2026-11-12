import { useLayoutEffect, useRef, useState } from "react";

import { Input } from "@/components/ui/input";
import { useT } from "@/i18n";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  LOW_STREAM_BITRATE_LIMIT_KBPS,
  STREAM_BITRATE_LIMIT_PRESETS_KBPS,
  formatStreamBitrateLimit,
  isLowStreamBitrateLimit,
  isStreamBitrateLimitPreset,
  parseStreamBitrateMbps,
  streamBitrateLimitMbpsText,
} from "@/lib/streamBitrateLimit";
import { TOUCH_TARGETS } from "@/lib/touchTargets";

const UNLIMITED = "0";
const CUSTOM = "custom";

function choiceFor(kbps: number | null): string {
  if (kbps === null) return "";
  return kbps === 0 || isStreamBitrateLimitPreset(kbps) ? String(kbps) : CUSTOM;
}

// A per-stream bitrate cap picked as Unlimited, a preset, or a custom Mbps
// value, stored in kbps. The caller owns the value (null = nothing picked yet);
// the choice and the raw custom text stay local so a cleared or half-typed box
// is an unsaved edit instead of a new cap. onValueChange(null) reports a custom
// box that holds no valid value.
export function StreamBitrateLimitInput({
  id,
  label,
  value,
  onValueChange,
  invalidDraft,
  onInvalidDraftChange,
  disabled = false,
}: {
  id: string;
  label: string;
  value: number | null;
  onValueChange: (kbps: number | null) => void;
  // The invalid custom text left behind when the input last unmounted, or
  // undefined. The input reopens on Custom with that text instead of `value`,
  // except that an empty draft with no value reopens with nothing picked.
  invalidDraft?: string;
  // Reports the custom text while it holds no valid value, and undefined once
  // it does or another choice is picked.
  onInvalidDraftChange?: (draft: string | undefined) => void;
  disabled?: boolean;
}) {
  const t = useT();
  const invalidMbps = t("web.player.bitrateLimit.invalid", { places: 3, min: 0 });
  const [choice, setChoice] = useState(() =>
    invalidDraft !== undefined && (invalidDraft !== "" || value !== null)
      ? CUSTOM
      : choiceFor(value),
  );
  const [draft, setDraft] = useState(
    () =>
      invalidDraft ??
      (value !== null && choiceFor(value) === CUSTOM ? streamBitrateLimitMbpsText(value) : ""),
  );
  const custom = choice === CUSTOM;
  const draftValue = parseStreamBitrateMbps(draft);
  const draftRef = useRef<HTMLInputElement>(null);

  // Surrounding forms block submission on this, so native validation and the
  // parser can't disagree about what is saveable, including for a draft
  // restored on mount.
  useLayoutEffect(() => {
    draftRef.current?.setCustomValidity(draftValue === null ? invalidMbps : "");
  }, [custom, draftValue, invalidMbps]);

  function handleChoiceChange(next: string) {
    if (next === "") return;
    setChoice(next);
    if (next !== CUSTOM) {
      onValueChange(Number(next));
      onInvalidDraftChange?.(undefined);
      return;
    }
    // Start from the current cap so picking Custom alone changes nothing.
    const seed = value !== null && value > 0 ? streamBitrateLimitMbpsText(value) : "";
    handleDraftChange(seed);
  }

  function handleDraftChange(raw: string) {
    const parsed = parseStreamBitrateMbps(raw);
    setDraft(raw);
    onValueChange(parsed);
    onInvalidDraftChange?.(parsed === null ? raw : undefined);
  }

  return (
    <div className="space-y-1">
      <div className="flex gap-2">
        <Select value={choice} onValueChange={handleChoiceChange} required disabled={disabled}>
          <SelectTrigger id={id} className={custom ? "w-32 shrink-0" : "w-full"}>
            <SelectValue placeholder={t("web.player.bitrateLimit.choosePlaceholder")} />
          </SelectTrigger>
          <SelectContent className={TOUCH_TARGETS}>
            <SelectItem value={UNLIMITED}>{t("web.player.bitrateLimit.unlimited")}</SelectItem>
            {STREAM_BITRATE_LIMIT_PRESETS_KBPS.map((kbps) => (
              <SelectItem key={kbps} value={String(kbps)}>
                {formatStreamBitrateLimit(kbps)}
              </SelectItem>
            ))}
            <SelectItem value={CUSTOM}>{t("web.player.bitrateLimit.custom")}</SelectItem>
          </SelectContent>
        </Select>
        {custom && (
          <div className="relative min-w-0 flex-1">
            <Input
              ref={draftRef}
              aria-label={t("web.player.bitrateLimit.customAria", { label })}
              inputMode="decimal"
              autoComplete="off"
              required
              aria-invalid={draft !== "" && draftValue === null}
              placeholder={t("web.player.bitrateLimit.customPlaceholder")}
              value={draft}
              onChange={(event) => handleDraftChange(event.target.value)}
              className="pe-14"
            />
            <span className="text-muted-foreground pointer-events-none absolute inset-y-0 end-3 flex items-center text-sm">
              {t("web.player.bitrateLimit.unit")}
            </span>
          </div>
        )}
      </div>
      {custom && draftValue === null && (
        <p className="text-muted-foreground text-xs">{invalidMbps}</p>
      )}
      {custom && draftValue !== null && isLowStreamBitrateLimit(draftValue) && (
        <p className="text-warning text-xs">
          {t("web.player.bitrateLimit.lowWarning", {
            mbps: LOW_STREAM_BITRATE_LIMIT_KBPS / 1000,
          })}
        </p>
      )}
    </div>
  );
}
