import type { ThemeVarOverrides } from "@/lib/themeTokens";

interface ThemePreviewCardProps {
  vars: ThemeVarOverrides;
}

/** A miniature UI mockup that renders with the given variable overrides applied inline. */
export function ThemePreviewCard({ vars }: ThemePreviewCardProps) {
  const v = (token: string, fallback: string) =>
    (vars as Record<string, string>)[token] ?? `var(--${token}, ${fallback})`;

  return (
    <div
      className="overflow-hidden rounded-2xl border"
      style={{
        backgroundColor: v("background", "#141417"),
        borderColor: v("border", "#2a2a2e"),
        color: v("foreground", "#e8e8ec"),
      }}
    >
      <div className="flex">
        {/* Sidebar mock */}
        <div
          className="hidden w-14 flex-col items-center gap-2 py-3 sm:flex"
          style={{ backgroundColor: v("sidebar", "#111114") }}
        >
          <div
            className="h-6 w-6 rounded-md"
            style={{ backgroundColor: v("sidebar-primary", "#e8e8ec") }}
          />
          <div
            className="h-5 w-5 rounded-md"
            style={{ backgroundColor: v("sidebar-accent", "#222226") }}
          />
          <div
            className="h-5 w-5 rounded-md"
            style={{ backgroundColor: v("sidebar-accent", "#222226") }}
          />
        </div>

        {/* Content area */}
        <div className="flex-1 p-3">
          {/* Nav bar mock */}
          <div className="mb-3 flex items-center gap-2">
            <div
              className="h-2 w-2.5 rounded-full"
              style={{ backgroundColor: v("primary", "#e8e8ec") }}
            />
            <div
              className="h-1.5 w-14 rounded-full"
              style={{ backgroundColor: v("foreground", "#e8e8ec"), opacity: 0.7 }}
            />
            <div className="flex-1" />
            <div
              className="h-1.5 w-8 rounded-full"
              style={{ backgroundColor: v("muted-foreground", "#71717a"), opacity: 0.5 }}
            />
          </div>

          {/* Cards row */}
          <div className="flex gap-2">
            <div
              className="flex-1 space-y-1.5 p-2"
              style={{
                backgroundColor: v("card", "#1c1c20"),
                borderRadius: v("radius", "0.5rem"),
              }}
            >
              <div
                className="h-1.5 w-16 rounded-full"
                style={{ backgroundColor: v("foreground", "#e8e8ec"), opacity: 0.8 }}
              />
              <div
                className="h-1.5 w-full rounded-full"
                style={{ backgroundColor: v("muted", "#27272a"), opacity: 0.6 }}
              />
              <div
                className="h-1.5 w-3/4 rounded-full"
                style={{ backgroundColor: v("muted", "#27272a"), opacity: 0.4 }}
              />
              <div className="mt-2 flex gap-1.5">
                <div
                  className="h-4 w-10 rounded-md"
                  style={{ backgroundColor: v("primary", "#e8e8ec") }}
                />
                <div
                  className="h-4 w-10 rounded-md"
                  style={{
                    backgroundColor: v("secondary", "#27272a"),
                  }}
                />
              </div>
            </div>
            <div
              className="w-16"
              style={{
                backgroundColor: v("accent", "#222226"),
                borderRadius: v("radius", "0.5rem"),
              }}
            >
              <div
                className="h-full min-h-14"
                style={{
                  background: `linear-gradient(180deg, ${v("primary", "#e8e8ec")}22, transparent)`,
                  borderRadius: v("radius", "0.5rem"),
                }}
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
