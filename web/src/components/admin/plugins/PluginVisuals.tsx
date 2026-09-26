import type { PluginCapability } from "@/api/types";
import { providerMonogram } from "@/lib/monogram";
import { pluginCapabilityKinds } from "@/lib/pluginCapabilities";
import { cn } from "@/lib/utils";

const MONOGRAM_SIZES = {
  sm: "size-7 rounded-lg text-[11px]",
  md: "size-9 rounded-lg text-[12.5px]",
  lg: "size-12 rounded-xl text-base",
} as const;

/** Two-letter square standing in for a plugin logo; manifests don't ship icons. */
export function PluginMonogram({
  name,
  size = "md",
  className,
}: {
  name: string;
  size?: keyof typeof MONOGRAM_SIZES;
  className?: string;
}) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "bg-surface-raised text-foreground/85 inline-flex shrink-0 items-center justify-center font-semibold tracking-wide",
        MONOGRAM_SIZES[size],
        className,
      )}
    >
      {providerMonogram(name)}
    </span>
  );
}

export function PluginStatusLabel({
  label,
  dotClass,
  textClass,
  title,
}: {
  label: string;
  dotClass: string;
  textClass?: string;
  title?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 text-[13px] whitespace-nowrap",
        textClass || "text-foreground/80",
      )}
      title={title}
    >
      <span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full", dotClass)} />
      {label}
    </span>
  );
}

/** One icon per primary capability, each named for screen readers and on hover. */
export function PluginCapabilityIcons({ capabilities }: { capabilities: PluginCapability[] }) {
  const kinds = pluginCapabilityKinds(capabilities);
  if (kinds.length === 0) return null;
  return (
    <span className="text-muted-foreground inline-flex items-center gap-2">
      {kinds.map(({ job, label, icon: Icon }) => (
        <span key={job} title={label} className="inline-flex">
          <Icon aria-hidden="true" className="size-4" />
          <span className="sr-only">{label}</span>
        </span>
      ))}
    </span>
  );
}
