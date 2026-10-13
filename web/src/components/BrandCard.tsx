import { Link } from "react-router";
import type { DiscoverBrandCard, DiscoverBrowseKind } from "@/api/types";
import { cn } from "@/lib/utils";

interface BrandCardProps {
  kind: DiscoverBrowseKind;
  card: DiscoverBrandCard;
  defaultMediaTypeForGenre?: "movie" | "series";
}

/** The browse page of a studio, network, or genre; a genre opens on movies unless asked otherwise. */
function brandBrowseHref(
  kind: DiscoverBrowseKind,
  card: DiscoverBrandCard,
  defaultMediaTypeForGenre: "movie" | "series" = "movie",
): string {
  const base = `/requests/browse/${kind}/${encodeURIComponent(card.slug)}`;
  if (kind !== "genre") return base;
  const initial =
    card.series_supported && defaultMediaTypeForGenre === "series" ? "series" : "movie";
  return `${base}?media_type=${initial}`;
}

export default function BrandCard({
  kind,
  card,
  defaultMediaTypeForGenre = "movie",
}: BrandCardProps) {
  const href = brandBrowseHref(kind, card, defaultMediaTypeForGenre);

  const baseClasses =
    "group relative flex h-28 w-52 flex-none transform-gpu cursor-pointer items-center justify-center overflow-hidden rounded-xl shadow-sm ring-1 transition duration-300 ease-in-out hover:scale-[1.03] focus:scale-[1.03] focus:outline-none sm:h-32 sm:w-64";

  if (kind === "genre") {
    const background = `linear-gradient(135deg, ${card.gradient_from ?? "#475569"}, ${card.gradient_to ?? "#0f172a"})`;
    return (
      <Link
        to={href}
        aria-label={card.display_name}
        className={cn(
          baseClasses,
          "ring-white/10 hover:ring-white/40 focus:ring-2 focus:ring-white",
        )}
        style={{ background }}
      >
        <span className="px-3 text-center text-base leading-tight font-semibold text-white drop-shadow">
          {card.display_name}
        </span>
      </Link>
    );
  }

  return (
    <Link
      to={href}
      aria-label={card.display_name}
      className={cn(
        baseClasses,
        "bg-gray-800 ring-gray-700 hover:bg-gray-700 hover:ring-gray-500 focus:ring-2 focus:ring-white",
      )}
    >
      {card.logo_url ? (
        <img
          src={card.logo_url}
          alt=""
          loading="lazy"
          className="h-full w-full object-contain px-6 py-7 sm:px-8 sm:py-8"
        />
      ) : (
        <span className="px-3 text-center text-base leading-tight font-semibold text-white">
          {card.display_name}
        </span>
      )}
    </Link>
  );
}
