import ViewTransitionLink from "@/components/ViewTransitionLink";

interface RelatedRailItem {
  content_id: string;
  title: string;
  poster_url?: string;
  subtitle?: string;
  highlight?: boolean;
}

interface RelatedRailProps {
  heading: string;
  subtitle?: string;
  items: RelatedRailItem[];
  coverAspect?: "square" | "poster";
}

export function RelatedRail({ heading, subtitle, items, coverAspect = "square" }: RelatedRailProps) {
  if (items.length === 0) return null;
  const coverAspectClass = coverAspect === "poster" ? "aspect-[2/3]" : "aspect-square";
  return (
    <section>
      <div className="mb-4">
        <h2 className="text-xl font-semibold tracking-tight">{heading}</h2>
        {subtitle && <p className="text-muted-foreground mt-1 text-xs">{subtitle}</p>}
      </div>
      <div className="-mx-2 flex gap-3 overflow-x-auto px-2 pb-2">
        {items.map((item) => (
          <ViewTransitionLink
            key={item.content_id}
            to={`/item/${item.content_id}`}
            className={`block w-[140px] shrink-0 sm:w-[160px] lg:w-[185px] ${
              item.highlight ? "ring-primary rounded-lg ring-2 ring-offset-2" : ""
            }`}
          >
            <div className={`bg-muted relative ${coverAspectClass} overflow-hidden rounded-lg`}>
              {item.poster_url ? (
                <img
                  src={item.poster_url}
                  alt={item.title}
                  className="h-full w-full object-cover"
                  loading="lazy"
                />
              ) : null}
            </div>
            <div className="mt-2 truncate text-sm font-medium">{item.title}</div>
            {item.subtitle && (
              <div className="text-muted-foreground truncate text-xs">{item.subtitle}</div>
            )}
          </ViewTransitionLink>
        ))}
      </div>
    </section>
  );
}
