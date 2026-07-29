import { cn } from "@/lib/utils";
import { MAX_CSS_SIZE } from "@/lib/themeExport";

interface RawCssEditorProps {
  value: string;
  onChange: (css: string) => void;
}

export function RawCssEditor({ value, onChange }: RawCssEditorProps) {
  const bytes = new TextEncoder().encode(value).length;
  const pct = Math.min(100, (bytes / MAX_CSS_SIZE) * 100);
  const isNearLimit = pct > 90;
  const isOverLimit = bytes > MAX_CSS_SIZE;

  return (
    <div className="space-y-3">
      <div className="space-y-1">
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          Write custom CSS that is injected after all theme variables. Target any selector — your
          overrides apply on top of the active theme. Use{" "}
          <code className="bg-muted rounded px-1 py-0.5 text-xs">:root</code> to override CSS custom
          properties directly.
        </p>
      </div>

      <textarea
        value={value}
        onChange={(e) => {
          const next = e.target.value;
          if (new TextEncoder().encode(next).length <= MAX_CSS_SIZE) {
            onChange(next);
          }
        }}
        placeholder={`/* Example: override the primary color */\n:root {\n  --primary: #ff6b6b;\n}\n\n/* Example: custom scrollbar */\n::-webkit-scrollbar {\n  width: 8px;\n}`}
        spellCheck={false}
        className={cn(
          "border-border bg-background text-foreground placeholder:text-muted-foreground/50 min-h-[240px] w-full resize-y rounded-xl border p-3 font-mono text-[13px] leading-relaxed focus:ring-2 focus:outline-none",
          isOverLimit ? "focus:ring-destructive" : "focus:ring-ring",
        )}
      />

      <div className="flex items-center justify-between">
        <p className="text-muted-foreground text-[11px]">
          Preview updates as you type. Saved automatically after you pause.
        </p>
        <span
          className={cn(
            "font-mono text-[11px]",
            isOverLimit
              ? "text-destructive"
              : isNearLimit
                ? "text-warning"
                : "text-muted-foreground",
          )}
        >
          {(bytes / 1024).toFixed(1)} / {MAX_CSS_SIZE / 1024} KB
        </span>
      </div>
    </div>
  );
}
