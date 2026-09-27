import { useCallback, useMemo } from "react";
import { RotateCcw } from "lucide-react";
import { Slider } from "@/components/ui/slider";
import {
  TOKEN_GROUP_ORDER,
  TOKEN_GROUPS,
  AVAILABLE_FONTS,
  getComputedToken,
} from "@/lib/themeTokens";
import type { ThemeToken, ThemeVarOverrides, TokenMeta } from "@/lib/themeTokens";
import { cn } from "@/lib/utils";

interface TokenEditorProps {
  vars: ThemeVarOverrides;
  onSetVar: (token: ThemeToken, value: string) => void;
  onResetVar: (token: ThemeToken) => void;
}

/** Attempt to convert any CSS color value to hex for the color input. */
function toHex(value: string): string {
  if (/^#[0-9a-f]{6}$/i.test(value)) return value;
  // Use a temporary element to resolve CSS colors to rgb
  try {
    const el = document.createElement("div");
    el.style.color = value;
    document.body.appendChild(el);
    const computed = getComputedStyle(el).color;
    document.body.removeChild(el);
    const match = computed.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
    if (match) {
      const [, r, g, b] = match;
      return `#${[r, g, b].map((c) => Number(c).toString(16).padStart(2, "0")).join("")}`;
    }
  } catch {
    // Ignore
  }
  return "#000000";
}

function ColorTokenInput({
  meta,
  value,
  computedValue,
  onSet,
  onReset,
}: {
  meta: TokenMeta;
  value: string | undefined;
  computedValue: string;
  onSet: (value: string) => void;
  onReset: () => void;
}) {
  const displayValue = value ?? computedValue;
  const hexValue = toHex(displayValue);
  const isOverridden = value !== undefined;

  return (
    <div className="flex items-center gap-3">
      <div className="relative">
        <input
          type="color"
          value={hexValue}
          onChange={(e) => onSet(e.target.value)}
          className="h-8 w-8 cursor-pointer rounded-lg border-0 bg-transparent p-0"
          title={meta.label}
        />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-[13px] font-medium">{meta.label}</span>
          {isOverridden && (
            <button
              type="button"
              onClick={onReset}
              className="text-muted-foreground hover:text-foreground"
              title="Reset to theme default"
            >
              <RotateCcw className="h-3 w-3" />
            </button>
          )}
        </div>
        <span className="text-muted-foreground font-mono text-[11px]">{displayValue}</span>
      </div>
    </div>
  );
}

function RadiusInput({
  value,
  computedValue,
  onSet,
  onReset,
}: {
  value: string | undefined;
  computedValue: string;
  onSet: (value: string) => void;
  onReset: () => void;
}) {
  const currentRem = parseFloat(value ?? computedValue) || 0.5;
  const isOverridden = value !== undefined;

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-[13px] font-medium">Border Radius</span>
          {isOverridden && (
            <button
              type="button"
              onClick={onReset}
              className="text-muted-foreground hover:text-foreground"
              title="Reset to theme default"
            >
              <RotateCcw className="h-3 w-3" />
            </button>
          )}
        </div>
        <span className="text-muted-foreground font-mono text-[11px]">{currentRem}rem</span>
      </div>
      <Slider
        value={[currentRem]}
        min={0}
        max={1.5}
        step={0.05}
        onValueChange={([v]) => onSet(`${v}rem`)}
      />
      <div className="flex justify-between">
        <div className="rounded-sm border p-2" style={{ borderRadius: "0rem" }}>
          <div className="bg-muted h-3 w-6" />
        </div>
        <div className="rounded-sm border p-2" style={{ borderRadius: `${currentRem}rem` }}>
          <div className="bg-primary h-3 w-6" style={{ borderRadius: `${currentRem}rem` }} />
        </div>
        <div className="rounded-sm border p-2" style={{ borderRadius: "1.5rem" }}>
          <div className="bg-muted h-3 w-6" style={{ borderRadius: "1.5rem" }} />
        </div>
      </div>
    </div>
  );
}

function FontInput({
  value,
  computedValue,
  onSet,
  onReset,
}: {
  value: string | undefined;
  computedValue: string;
  onSet: (value: string) => void;
  onReset: () => void;
}) {
  const currentFont = value ?? computedValue.split(",")[0]?.replace(/["']/g, "").trim() ?? "Outfit";
  const isOverridden = value !== undefined;

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <span className="text-[13px] font-medium">Font Family</span>
        {isOverridden && (
          <button
            type="button"
            onClick={onReset}
            className="text-muted-foreground hover:text-foreground"
            title="Reset to theme default"
          >
            <RotateCcw className="h-3 w-3" />
          </button>
        )}
      </div>
      <div className="flex flex-wrap gap-2">
        {AVAILABLE_FONTS.map((font) => (
          <button
            key={font}
            type="button"
            onClick={() => onSet(`"${font}", sans-serif`)}
            className={cn(
              "rounded-xl border px-3 py-1.5 text-sm transition-colors",
              currentFont.includes(font)
                ? "border-primary bg-primary/10 text-foreground"
                : "border-border text-muted-foreground hover:border-primary/30 hover:text-foreground",
            )}
            style={{ fontFamily: `"${font}", sans-serif` }}
          >
            {font}
          </button>
        ))}
      </div>
    </div>
  );
}

export function TokenEditor({ vars, onSetVar, onResetVar }: TokenEditorProps) {
  // The base theme is fixed, so the painted values only need reading once.
  const computedValues = useMemo(() => {
    const map: Partial<Record<ThemeToken, string>> = {};
    for (const group of Object.values(TOKEN_GROUPS)) {
      for (const meta of group) {
        map[meta.token] = getComputedToken(meta.token);
      }
    }
    return map;
  }, []);

  const handleSet = useCallback(
    (token: ThemeToken) => (value: string) => onSetVar(token, value),
    [onSetVar],
  );
  const handleReset = useCallback((token: ThemeToken) => () => onResetVar(token), [onResetVar]);

  return (
    <div className="space-y-6">
      {TOKEN_GROUP_ORDER.map((groupName) => {
        const tokens = TOKEN_GROUPS[groupName];
        if (!tokens) return null;

        return (
          <div key={groupName}>
            <h4 className="text-muted-foreground mb-3 text-xs font-semibold tracking-wider uppercase">
              {groupName}
            </h4>
            <div className="space-y-3">
              {tokens.map((meta) => {
                if (meta.inputType === "color") {
                  return (
                    <ColorTokenInput
                      key={meta.token}
                      meta={meta}
                      value={vars[meta.token]}
                      computedValue={computedValues[meta.token] ?? ""}
                      onSet={handleSet(meta.token)}
                      onReset={handleReset(meta.token)}
                    />
                  );
                }
                if (meta.inputType === "radius") {
                  return (
                    <RadiusInput
                      key={meta.token}
                      value={vars[meta.token]}
                      computedValue={computedValues[meta.token] ?? "0.5rem"}
                      onSet={handleSet(meta.token)}
                      onReset={handleReset(meta.token)}
                    />
                  );
                }
                if (meta.inputType === "font") {
                  return (
                    <FontInput
                      key={meta.token}
                      value={vars[meta.token]}
                      computedValue={computedValues[meta.token] ?? '"Outfit", sans-serif'}
                      onSet={handleSet(meta.token)}
                      onReset={handleReset(meta.token)}
                    />
                  );
                }
                return null;
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
}
