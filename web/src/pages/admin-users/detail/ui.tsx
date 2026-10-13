import { useId, type ReactNode } from "react";

import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";

/**
 * Shared building blocks for the account page's cards. They follow the app's
 * surface-panel look; nothing here knows about accounts.
 */

export function DetailCard({
  title,
  description,
  actions,
  footer,
  editing = false,
  className,
  children,
  headingId,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  editing?: boolean;
  className?: string;
  children?: ReactNode;
  headingId?: string;
}) {
  const generatedId = useId();
  const titleId = headingId ?? generatedId;
  return (
    <section
      aria-labelledby={titleId}
      data-editing={editing ? "true" : undefined}
      className={cn(
        "surface-panel min-w-0 overflow-hidden rounded-2xl border-0",
        editing && "ring-1 ring-amber-500/45",
        className,
      )}
    >
      <div className="border-border/70 flex flex-wrap items-start justify-between gap-x-3 gap-y-2 border-b px-4 py-3.5 sm:px-5">
        <div className="min-w-0 flex-1 basis-48 space-y-0.5">
          <h3 id={titleId} className="text-sm leading-snug font-semibold">
            {title}
          </h3>
          {description ? (
            <div className="text-muted-foreground text-[13px] leading-relaxed">{description}</div>
          ) : null}
        </div>
        {actions ? <div className="flex shrink-0 items-center gap-1.5">{actions}</div> : null}
      </div>
      {children}
      {footer ? (
        <div className="border-border/70 flex flex-wrap items-center gap-2 border-t px-4 py-3 sm:px-5">
          {footer}
        </div>
      ) : null}
    </section>
  );
}

/** A strip of stat tiles: 2 columns on narrow screens, `columns` from `lg` (4 from `sm`). */
export function StatStrip({ children, columns = 5 }: { children: ReactNode; columns?: 4 | 5 }) {
  return (
    <div
      className={cn(
        "surface-panel grid grid-cols-2 gap-px overflow-hidden rounded-2xl border-0 bg-transparent",
        // A last tile left alone on its row spans it.
        "[&>*:last-child:nth-child(odd)]:col-span-2",
        columns === 5
          ? "lg:grid-cols-5 lg:[&>*:last-child:nth-child(odd)]:col-span-1"
          : "sm:grid-cols-4 sm:[&>*:last-child:nth-child(odd)]:col-span-1",
      )}
    >
      {children}
    </div>
  );
}

export function StatTile({
  label,
  value,
  total,
  detail,
  progress,
}: {
  label: string;
  value: ReactNode;
  total?: ReactNode;
  detail?: ReactNode;
  /** 0..1; shows a bar under the value. */
  progress?: number;
}) {
  return (
    <div className="bg-card/60 min-w-0 space-y-1.5 px-4 py-3.5">
      <div className="text-muted-foreground truncate text-xs">{label}</div>
      <div className="text-xl font-semibold tabular-nums">
        {value}
        {total !== undefined ? (
          <span className="text-muted-foreground ml-1 text-sm font-normal">of {total}</span>
        ) : null}
      </div>
      {progress !== undefined ? <ProgressBar value={progress} label={label} /> : null}
      {detail ? <div className="text-muted-foreground text-xs break-words">{detail}</div> : null}
    </div>
  );
}

/**
 * One labeled value: the label (and description) on the left, the value on
 * the right with an optional line under it and a source tag. On a narrow
 * screen a wide value drops under the label.
 */
export function KeyValueRow({
  label,
  description,
  value,
  below,
  tag,
  changed = false,
}: {
  label: ReactNode;
  description?: ReactNode;
  value: ReactNode;
  below?: ReactNode;
  tag?: ReactNode;
  changed?: boolean;
}) {
  return (
    <div
      data-changed={changed ? "true" : undefined}
      className={cn(
        "border-border/50 flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t px-4 py-3 first:border-t-0 sm:px-5",
        changed && "bg-amber-500/[0.04] shadow-[inset_3px_0_0_var(--color-amber-500)]",
      )}
    >
      <div className="min-w-0 flex-1 basis-28">
        <div className="text-muted-foreground text-sm">{label}</div>
        {description ? (
          <div className="text-muted-foreground/80 text-xs leading-relaxed">{description}</div>
        ) : null}
      </div>
      <div className="ml-auto flex max-w-full min-w-0 items-center justify-end gap-2.5">
        <div className="flex min-w-0 flex-col items-end text-right">
          <div className="text-sm font-medium break-words">{value}</div>
          {below ? <div className="text-muted-foreground text-xs">{below}</div> : null}
        </div>
        {tag}
      </div>
    </div>
  );
}

const SOURCE_LABELS = { default: "DEFAULT", group: "GROUP", custom: "CUSTOM" } as const;

/** Where a value comes from: the server default, the account's group, or this account. */
export function SourceTag({ source }: { source: "default" | "group" | "custom" }) {
  return (
    <span
      data-source={source}
      className={cn(
        "shrink-0 text-[10px] font-semibold tracking-[0.06em] uppercase",
        source === "custom"
          ? "rounded-md border border-amber-500/30 bg-amber-500/10 px-1.5 py-px text-amber-300"
          : "text-muted-foreground/80",
      )}
    >
      {SOURCE_LABELS[source]}
    </span>
  );
}

/** A small pill switcher for views inside a tab. */
export function SubTabs<T extends string>({
  value,
  onValueChange,
  items,
  ariaLabel,
}: {
  value: T;
  onValueChange: (value: T) => void;
  items: { value: T; label: ReactNode }[];
  ariaLabel: string;
}) {
  return (
    <Tabs value={value} onValueChange={(next) => onValueChange(next as T)} className="min-w-0">
      <TabsList
        aria-label={ariaLabel}
        className="surface-panel-subtle h-auto max-w-full justify-start overflow-x-auto rounded-xl p-1"
      >
        {items.map((item) => (
          <TabsTrigger
            key={item.value}
            value={item.value}
            className="flex-none rounded-lg px-3 py-1.5 text-[13px]"
          >
            {item.label}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  );
}

export function ListRow({
  leading,
  title,
  meta,
  trailing,
}: {
  leading?: ReactNode;
  title: ReactNode;
  meta?: ReactNode;
  trailing?: ReactNode;
}) {
  return (
    <div className="border-border/50 flex items-center gap-3 border-t px-4 py-3 first:border-t-0 sm:px-5">
      {leading ? <div className="shrink-0">{leading}</div> : null}
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{title}</div>
        {meta ? <div className="text-muted-foreground text-xs">{meta}</div> : null}
      </div>
      {trailing ? (
        <div className="text-muted-foreground shrink-0 text-right text-xs">{trailing}</div>
      ) : null}
    </div>
  );
}

/** A thin bar. With a label it is announced as a progress bar; without one it is decoration. */
export function ProgressBar({
  value,
  className,
  label,
}: {
  value: number;
  className?: string;
  label?: string;
}) {
  const clamped = Number.isFinite(value) ? Math.min(1, Math.max(0, value)) : 0;
  const percent = Math.round(clamped * 100);
  return (
    <div
      className={cn("bg-foreground/10 h-1 overflow-hidden rounded-full", className)}
      {...(label
        ? {
            role: "progressbar",
            "aria-label": label,
            "aria-valuemin": 0,
            "aria-valuemax": 100,
            "aria-valuenow": percent,
          }
        : { "aria-hidden": true })}
    >
      <div className="h-full rounded-full bg-amber-500" style={{ width: `${percent}%` }} />
    </div>
  );
}

const AVATAR_GRADIENTS = [
  "from-rose-500 to-orange-400",
  "from-indigo-500 to-violet-500",
  "from-emerald-500 to-teal-500",
  "from-amber-500 to-orange-600",
  "from-sky-500 to-blue-600",
] as const;

/** A round initial on one of five gradients, picked by `seed`. */
export function InitialAvatar({
  name,
  seed,
  size = "sm",
}: {
  name: string;
  seed: number;
  size?: "sm" | "lg";
}) {
  const gradient = AVATAR_GRADIENTS[Math.abs(Math.trunc(seed)) % AVATAR_GRADIENTS.length];
  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded-full bg-gradient-to-br font-semibold text-white uppercase",
        gradient,
        size === "lg" ? "size-14 text-2xl sm:size-16 sm:text-3xl" : "size-8 text-sm",
      )}
    >
      {name.trim().charAt(0) || "?"}
    </span>
  );
}
