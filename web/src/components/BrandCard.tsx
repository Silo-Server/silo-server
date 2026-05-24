import { useNavigate } from "react-router";
import type { DiscoverBrandCard, DiscoverBrowseKind } from "@/api/types";
import { cn } from "@/lib/utils";

interface BrandCardProps {
  kind: DiscoverBrowseKind;
  card: DiscoverBrandCard;
  defaultMediaTypeForGenre?: "movie" | "series";
}

export default function BrandCard({
  kind,
  card,
  defaultMediaTypeForGenre = "movie",
}: BrandCardProps) {
  const navigate = useNavigate();
  const isGenre = kind === "genre";
  const background = isGenre
    ? `linear-gradient(135deg, ${card.gradient_from ?? "#475569"}, ${card.gradient_to ?? "#0f172a"})`
    : card.brand_color || "#1f2937";

  function handleClick() {
    const base = `/requests/browse/${kind}/${encodeURIComponent(card.slug)}`;
    if (kind === "genre") {
      const initial =
        card.series_supported && defaultMediaTypeForGenre === "series" ? "series" : "movie";
      navigate(`${base}?media_type=${initial}`);
      return;
    }
    navigate(base);
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      aria-label={card.display_name}
      className={cn(
        "group relative flex h-20 w-[140px] flex-none items-center justify-center overflow-hidden rounded-lg shadow-sm",
        "ring-1 ring-white/5 transition-colors hover:ring-white/30 focus:outline-none focus:ring-2 focus:ring-white",
      )}
      style={{ background }}
    >
      {!isGenre && card.logo_url ? (
        <img
          src={card.logo_url}
          alt={card.display_name}
          loading="lazy"
          className="max-h-[60%] max-w-[80%] object-contain"
        />
      ) : (
        <span className="px-2 text-center text-sm leading-tight font-semibold text-white drop-shadow">
          {card.display_name}
        </span>
      )}
    </button>
  );
}
