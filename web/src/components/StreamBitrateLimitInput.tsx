import { useState } from "react";

import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  STREAM_BITRATE_LIMIT_PRESETS_KBPS,
  formatStreamBitrateLimit,
  isLowStreamBitrateLimit,
  isStreamBitrateLimitPreset,
  parseStreamBitrateMbps,
  streamBitrateLimitMbpsText,
} from "@/lib/streamBitrateLimit";

const UNLIMITED = "0";
const CUSTOM = "custom";
const INVALID_MBPS = "Enter a value above 0 Mbps, such as 8 or 1.5 (up to 3 decimal places).";

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
}: {
  id: string;
  label: string;
  value: number | null;
  onValueChange: (kbps: number | null) => void;
}) {
  const [choice, setChoice] = useState(() => choiceFor(value));
  const [draft, setDraft] = useState(() =>
    value !== null && choiceFor(value) === CUSTOM ? streamBitrateLimitMbpsText(value) : "",
  );
  const custom = choice === CUSTOM;
  const draftValue = parseStreamBitrateMbps(draft);

  function handleChoiceChange(next: string) {
    if (next === "") return;
    setChoice(next);
    if (next !== CUSTOM) {
      onValueChange(Number(next));
      return;
    }
    // Start from the current cap so picking Custom alone changes nothing.
    const seed = value !== null && value > 0 ? streamBitrateLimitMbpsText(value) : "";
    setDraft(seed);
    onValueChange(parseStreamBitrateMbps(seed));
  }

  function handleDraftChange(input: HTMLInputElement) {
    const parsed = parseStreamBitrateMbps(input.value);
    // Surrounding forms block submission on this, so native validation and the
    // parser can't disagree about what is saveable.
    input.setCustomValidity(parsed === null ? INVALID_MBPS : "");
    setDraft(input.value);
    onValueChange(parsed);
  }

  return (
    <div className="space-y-1">
      <div className="flex gap-2">
        <Select value={choice} onValueChange={handleChoiceChange} required>
          <SelectTrigger id={id} className={custom ? "w-32 shrink-0" : "w-full"}>
            <SelectValue placeholder="Choose a limit" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={UNLIMITED}>Unlimited</SelectItem>
            {STREAM_BITRATE_LIMIT_PRESETS_KBPS.map((kbps) => (
              <SelectItem key={kbps} value={String(kbps)}>
                {formatStreamBitrateLimit(kbps)}
              </SelectItem>
            ))}
            <SelectItem value={CUSTOM}>Custom</SelectItem>
          </SelectContent>
        </Select>
        {custom && (
          <div className="relative min-w-0 flex-1">
            <Input
              aria-label={`${label} in Mbps`}
              inputMode="decimal"
              autoComplete="off"
              required
              aria-invalid={draft !== "" && draftValue === null}
              placeholder="e.g. 1.5"
              value={draft}
              onChange={(event) => handleDraftChange(event.target)}
              className="pr-14"
            />
            <span className="text-muted-foreground pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm">
              Mbps
            </span>
          </div>
        )}
      </div>
      {custom && draftValue === null && (
        <p className="text-muted-foreground text-xs">{INVALID_MBPS}</p>
      )}
      {custom && draftValue !== null && isLowStreamBitrateLimit(draftValue) && (
        <p className="text-warning text-xs">
          Below 1 Mbps, streams are transcoded to very low quality.
        </p>
      )}
    </div>
  );
}
