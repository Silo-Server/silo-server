// The episode order dialog's header controls: where the episodes come from
// (provider and order), which order the file names follow, and the season
// layout.
import { useState } from "react";
import { Check, ChevronDown } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import type {
  EpisodeOrderOption,
  EpisodeOrderPreview,
  EpisodeOrderState,
} from "@/hooks/queries/episodeOrder";
import { useT, type MessageKey, type Translate } from "@/i18n";
import {
  asProvider,
  episodeOrderLabel,
  fileOrderLabel,
  filesPlacedBy,
  otherProvider,
  providerLabel,
  PROVIDERS,
  type Provider,
} from "@/lib/episodeOrder";
import { cn } from "@/lib/utils";

export type Source = "library" | Provider;
export type Layout = "seasons" | "absolute" | "custom";

export interface Draft {
  source: Source;
  /** An order of the source provider; empty is its default order. */
  orderId: string;
  /** The provider whose default order the file names follow. */
  files: Provider;
  layout: Layout;
}

const KIND_KEY: Record<string, MessageKey> = {
  aired: "web.item.episodeOrder.kind.aired",
  dvd: "web.item.episodeOrder.kind.dvd",
  absolute: "web.item.episodeOrder.kind.absolute",
  digital: "web.item.episodeOrder.kind.digital",
  story_arc: "web.item.episodeOrder.kind.storyArc",
  production: "web.item.episodeOrder.kind.production",
  tv: "web.item.episodeOrder.kind.tv",
};

function orderMeta(t: Translate, order: EpisodeOrderOption) {
  const meta = [t(KIND_KEY[order.kind] ?? "web.item.episodeOrder.kind.alternate")];
  if (order.season_count > 0)
    meta.push(t("web.item.episodeOrder.order.seasons", { count: order.season_count }));
  if (order.episode_count > 0)
    meta.push(t("web.item.episodeOrder.order.episodes", { count: order.episode_count }));
  return meta.join(" · ");
}

/** A header control: a small caption over its value. */
const fieldClass =
  "border-border bg-surface hover:border-foreground/30 data-[state=open]:border-foreground/30 flex w-full flex-col items-start rounded-xl sm:w-auto sm:min-w-[15rem] border px-3 py-1.5 text-start";

function FieldCaption({ children }: { children: string }) {
  return (
    <span className="text-muted-foreground text-[0.625rem] font-medium tracking-wide uppercase">
      {children}
    </span>
  );
}

export function OrderPicker({
  state,
  draft,
  structure,
  basedOn,
  onChange,
}: {
  state: EpisodeOrderState;
  draft: Draft;
  structure: Provider;
  /** The Custom layout: the order is what the arrangement starts from. */
  basedOn: boolean;
  onChange: (patch: Partial<Draft>) => void;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const libraryProvider = asProvider(state.library_provider);
  const entry = state.providers.find((p) => p.provider === structure);
  const orders = entry?.orders ?? [];
  const defaultOrder = orders.find((o) => o.is_default);
  const selected =
    draft.source === "library"
      ? defaultOrder
      : (orders.find((o) => o.id === draft.orderId) ??
        (draft.orderId === "" ? defaultOrder : undefined));
  const label = episodeOrderLabel(
    t,
    structure,
    draft.source === "library" || selected?.is_default ? "" : draft.orderId,
    selected?.name,
  );

  const sourceOption = (source: Source, title: string, body: string, disabled = false) => (
    <button
      key={source}
      type="button"
      role="radio"
      aria-checked={draft.source === source}
      disabled={disabled}
      onClick={() =>
        onChange(
          source === "library"
            ? { source, orderId: "" }
            : {
                source,
                orderId: "",
                // Files keep the naming they had unless it followed the
                // provider being replaced.
                files: draft.files === structure ? source : draft.files,
              },
        )
      }
      className={cn(
        "flex w-full flex-col rounded-lg border border-transparent px-3 py-2 text-start disabled:opacity-50",
        draft.source === source ? "bg-primary/10 border-primary/30" : "hover:bg-accent/50",
      )}
    >
      <span className="text-sm font-medium">{title}</span>
      <span className="text-muted-foreground text-xs leading-snug">{body}</span>
    </button>
  );

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger className={fieldClass}>
        <FieldCaption>
          {t(basedOn ? "web.item.episodeOrder.basedOn" : "web.item.episodeOrder.source.label")}
        </FieldCaption>
        <span className="flex w-full items-center gap-2 text-sm font-semibold">
          <span className="truncate">
            {draft.source === "library"
              ? t("web.item.episodeOrder.summaryLibrary", { summary: label })
              : label}
          </span>
          <ChevronDown
            className="text-muted-foreground ms-auto size-4 shrink-0"
            aria-hidden="true"
          />
        </span>
      </PopoverTrigger>
      <PopoverContent className="w-[min(40rem,calc(100vw-2rem))] p-2">
        <div className="grid gap-2 sm:grid-cols-2">
          <div role="radiogroup" aria-label={t("web.item.episodeOrder.source.label")}>
            <p className="text-muted-foreground px-3 py-1.5 text-[0.6875rem] font-medium tracking-wide uppercase">
              {t("web.item.episodeOrder.source.label")}
            </p>
            {sourceOption(
              "library",
              t("web.item.episodeOrder.source.library", {
                provider: providerLabel(libraryProvider),
              }),
              t("web.item.episodeOrder.source.libraryBody", {
                provider: providerLabel(libraryProvider),
                other: providerLabel(otherProvider(libraryProvider)),
              }),
            )}
            {PROVIDERS.map((p) =>
              sourceOption(
                p,
                providerLabel(p),
                t(
                  p === "tvdb"
                    ? "web.item.episodeOrder.source.tvdbBody"
                    : "web.item.episodeOrder.source.tmdbBody",
                ),
                !state.providers.some((e) => e.provider === p),
              ),
            )}
          </div>
          <div role="radiogroup" aria-label={t("web.item.episodeOrder.order.label")}>
            <p className="text-muted-foreground px-3 py-1.5 text-[0.6875rem] font-medium tracking-wide uppercase">
              {t("web.item.episodeOrder.order.label")}
            </p>
            {orders.map((o) => {
              const on = o.id === selected?.id;
              return (
                <button
                  key={o.id}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  disabled={draft.source === "library"}
                  onClick={() => {
                    onChange({ orderId: o.is_default ? "" : o.id });
                    setOpen(false);
                  }}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-lg px-3 py-2 text-start disabled:opacity-60",
                    on ? "bg-accent" : "hover:bg-accent/50",
                  )}
                >
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">{o.name}</span>
                    <span className="text-muted-foreground block text-xs">{orderMeta(t, o)}</span>
                  </span>
                  {on && <Check className="text-primary size-4 shrink-0" aria-hidden="true" />}
                </button>
              );
            })}
            <p className="text-muted-foreground px-3 py-2 text-xs leading-relaxed">
              {entry?.failed
                ? t("web.item.episodeOrder.order.failed")
                : entry && !entry.supported
                  ? t("web.item.episodeOrder.order.unsupported")
                  : draft.source === "library"
                    ? t("web.item.episodeOrder.order.helpLibrary")
                    : t("web.item.episodeOrder.order.help")}
            </p>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

function FitBadge({ matched, total }: { matched: number; total: number }) {
  const t = useT();
  const all = matched === total;
  return (
    <span
      className={cn(
        "rounded-full px-1.5 py-px text-[0.6875rem] font-semibold whitespace-nowrap",
        all ? "bg-success/15 text-success-label" : "bg-warning/15 text-warning-label",
      )}
    >
      {all
        ? t("web.item.episodeOrder.files.fitsAll")
        : t("web.item.episodeOrder.files.fit", { matched, total })}
    </span>
  );
}

export function FilesPicker({
  files,
  structure,
  preview,
  onChange,
}: {
  files: Provider;
  structure: Provider;
  preview: EpisodeOrderPreview | undefined;
  onChange: (files: Provider) => void;
}) {
  const t = useT();
  const fit = (p: Provider) => {
    const placed = filesPlacedBy(preview, p, structure);
    return preview && preview.file_count > 0 && placed !== undefined ? (
      <FitBadge matched={placed} total={preview.file_count} />
    ) : null;
  };
  return (
    <Select value={files} onValueChange={(v) => onChange(asProvider(v))}>
      <SelectTrigger
        className={cn(fieldClass, "h-auto! flex-row items-center gap-2")}
        aria-label={t("web.item.episodeOrder.files.label")}
        title={t("web.item.episodeOrder.files.help")}
      >
        <span className="flex min-w-0 flex-1 flex-col items-start">
          <FieldCaption>{t("web.item.episodeOrder.files.label")}</FieldCaption>
          <span className="flex items-center gap-2 text-sm font-semibold">
            {fileOrderLabel(t, files)}
            {fit(files)}
          </span>
        </span>
      </SelectTrigger>
      <SelectContent position="popper" align="start">
        {PROVIDERS.map((p) => (
          <SelectItem key={p} value={p}>
            <span className="flex items-center gap-2">
              <span>{fileOrderLabel(t, p)}</span>
              {fit(p)}
            </span>
          </SelectItem>
        ))}
        <p className="text-muted-foreground max-w-80 px-2 py-1.5 text-xs leading-relaxed">
          {t("web.item.episodeOrder.files.help")}
        </p>
      </SelectContent>
    </Select>
  );
}

export function LayoutSwitch({
  layout,
  structure,
  onChange,
  onCustomIntent,
}: {
  layout: Layout;
  structure: Provider;
  onChange: (layout: Layout) => void;
  /** The pointer or focus is on Custom: its arrangement can start loading. */
  onCustomIntent: () => void;
}) {
  const t = useT();
  const options: { value: Layout; label: string; help: string }[] = [
    {
      value: "seasons",
      label: t("web.item.episodeOrder.layout.seasons"),
      help: t("web.item.episodeOrder.layout.seasonsBody", { provider: providerLabel(structure) }),
    },
    {
      value: "absolute",
      label: t("web.item.episodeOrder.layout.absolute"),
      help: t("web.item.episodeOrder.layout.absoluteBody"),
    },
    {
      value: "custom",
      label: t("web.item.episodeOrder.layout.custom"),
      help: t("web.item.episodeOrder.layout.customBody"),
    },
  ];
  return (
    <div
      role="radiogroup"
      aria-label={t("web.item.episodeOrder.layout.label")}
      className="border-border bg-surface flex w-full items-stretch gap-0.5 rounded-xl border p-1 sm:w-auto"
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={layout === o.value}
          title={o.help}
          onClick={() => onChange(o.value)}
          onPointerEnter={o.value === "custom" ? onCustomIntent : undefined}
          onFocus={o.value === "custom" ? onCustomIntent : undefined}
          className={cn(
            "min-h-9 flex-1 rounded-lg px-3.5 text-sm font-semibold sm:flex-none",
            layout === o.value
              ? "bg-foreground text-background"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
