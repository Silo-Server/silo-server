import { useId, type ReactNode } from "react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";

import { RadioDot } from "@/components/ui/radio-group";
import { mediaKindLabel, type CollectionTemplate } from "@/lib/collectionTemplates";
import { useT, type Translate } from "@/i18n";

const SOURCE_TAG: Partial<Record<CollectionTemplate["source"], string>> = {
  mdblist: "MDBList",
  tmdb: "TMDB",
  tmdb_list: "TMDB",
};

/** "syncs daily", "syncs weekly": how often a template's cron syncs, for its meta line. */
function scheduleDescription(t: Translate, cron: string): string {
  const syncs = (schedule: string) => t("web.collections.templateGallery.syncs", { schedule });
  const [, hour, dom, month, dow, extra] = cron.trim().split(/\s+/);
  if (!hour || !dom || !month || !dow || extra !== undefined) return syncs("other");
  const stepMatch = hour.match(/^\*\/(\d+)$/);
  if (stepMatch)
    return t("web.collections.templateGallery.syncsEveryHours", {
      count: Number(stepMatch[1]),
      hours: stepMatch[1]!,
    });
  if (hour === "*") return syncs("hourly");
  if (dom === "1" && month === "*") return syncs("monthly");
  if (dom === "*" && month === "*" && dow === "*") return syncs("daily");
  if (dom === "*" && month === "*") return syncs("weekly");
  return syncs("other");
}

/**
 * One list in the Synced list step: a radio row with a small poster, the
 * name, a source tag and a meta line. Must sit inside a radio group.
 */
export function PickRow({
  value,
  title,
  tag,
  poster,
  meta,
}: {
  value: string;
  title: string;
  tag?: string;
  poster?: ReactNode;
  meta: string;
}) {
  const id = useId();
  return (
    <RadioGroupPrimitive.Item
      value={value}
      aria-labelledby={`${id}-title`}
      aria-describedby={tag ? `${id}-tag ${id}-meta` : `${id}-meta`}
      className="group/radio hover:bg-accent/60 focus-visible:ring-ring/50 data-[state=checked]:bg-accent grid w-full grid-cols-[18px_30px_minmax(0,1fr)] items-center gap-x-3 px-3.5 py-2.5 text-start outline-none focus-visible:ring-[3px] focus-visible:ring-inset"
    >
      <RadioDot />
      <span
        aria-hidden
        className="bg-muted grid aspect-[2/3] w-[30px] place-items-center overflow-hidden rounded-[5px] text-base"
      >
        {poster}
      </span>
      <span className="grid min-w-0 gap-0.5">
        <span className="flex min-w-0 items-center gap-2">
          <span id={`${id}-title`} className="truncate text-[14.5px] font-semibold">
            {title}
          </span>
          {tag ? (
            <span
              id={`${id}-tag`}
              className="bg-muted text-muted-foreground shrink-0 rounded px-1.5 py-px text-[10.5px] font-semibold tracking-wide uppercase"
            >
              {tag}
            </span>
          ) : null}
        </span>
        <span id={`${id}-meta`} className="text-muted-foreground truncate text-[12.5px]">
          {meta}
        </span>
      </span>
    </RadioGroupPrimitive.Item>
  );
}

/** A ready-made pick: a template that names its list. */
export function CollectionTemplateCard({ template }: { template: CollectionTemplate }) {
  const t = useT();
  const meta = [
    mediaKindLabel(template.media_kind),
    template.default_limit
      ? t("web.collections.metaLine.titleCount", {
          count: template.default_limit,
          shown: template.default_limit,
        })
      : null,
    template.default_sync_schedule ? scheduleDescription(t, template.default_sync_schedule) : null,
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <PickRow
      value={template.id}
      title={template.title}
      tag={SOURCE_TAG[template.source]}
      meta={meta}
      poster={
        template.poster_path ? (
          <img
            src={template.poster_path}
            alt=""
            loading="lazy"
            className="size-full object-cover"
          />
        ) : (
          template.icon
        )
      }
    />
  );
}
