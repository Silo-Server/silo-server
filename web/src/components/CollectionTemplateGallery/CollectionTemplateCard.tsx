import { Badge } from "@/components/ui/badge";
import { mediaKindLabel, type CollectionTemplate } from "@/lib/collectionTemplates";

const SOURCE_LABEL: Record<CollectionTemplate["source"], string> = {
  tmdb: "TMDB",
  trakt: "Trakt",
  mdblist: "MDBList",
  tmdb_discover: "TMDB Discover",
  tmdb_collection: "TMDB Franchise",
};

interface Props {
  template: CollectionTemplate;
  onPick: (template: CollectionTemplate) => void;
}

export function CollectionTemplateCard({ template, onPick }: Props) {
  return (
    <button
      type="button"
      onClick={() => onPick(template)}
      className="border-border hover:border-primary hover:bg-accent bg-card flex h-full flex-col rounded-md border p-4 text-left transition-colors"
    >
      <div className="flex items-start justify-between gap-3">
        <div
          className="bg-primary/10 text-primary flex h-10 w-10 shrink-0 items-center justify-center rounded-md text-xl"
          aria-hidden
        >
          {template.icon}
        </div>
        <div className="flex flex-wrap items-center justify-end gap-1">
          <TemplateBadges template={template} />
        </div>
      </div>
      <div className="mt-3 flex flex-1 flex-col gap-3">
        <div>
          <p className="text-sm leading-tight font-medium">{template.title}</p>
          <p className="text-muted-foreground mt-1 line-clamp-3 text-xs">{template.description}</p>
        </div>
        <div className="text-muted-foreground mt-auto flex items-center gap-2 text-[11px]">
          <span>{mediaKindLabel(template.media_kind)}</span>
          {template.default_sync_schedule ? (
            <>
              <span aria-hidden>•</span>
              <span className="truncate">
                syncs {scheduleDescription(template.default_sync_schedule)}
              </span>
            </>
          ) : null}
        </div>
      </div>
    </button>
  );
}

function TemplateBadges({ template }: { template: CollectionTemplate }) {
  return (
    <>
      <Badge variant="outline" className="bg-background/80 text-[10px] tracking-wide uppercase">
        {SOURCE_LABEL[template.source]}
      </Badge>
      {template.requires_profile ? (
        <Badge variant="secondary" className="text-[10px]">
          Profile
        </Badge>
      ) : null}
    </>
  );
}

function scheduleDescription(cron: string): string {
  const [, hour, dom, month, dow, extra] = cron.trim().split(/\s+/);
  if (!hour || !dom || !month || !dow || extra !== undefined) return "on schedule";
  const stepMatch = hour.match(/^\*\/(\d+)$/);
  if (stepMatch) return `every ${stepMatch[1]} hours`;
  if (hour === "*") return "hourly";
  if (dom === "1" && month === "*") return "monthly";
  if (dom === "*" && month === "*" && dow === "*") return "daily";
  if (dom === "*" && month === "*") return "weekly";
  return "on schedule";
}
