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

  const baseClasses =
    "group relative flex h-28 w-52 flex-none transform-gpu cursor-pointer items-center justify-center overflow-hidden rounded-xl shadow-sm ring-1 transition duration-300 ease-in-out hover:scale-[1.03] focus:scale-[1.03] focus:outline-none sm:h-32 sm:w-64";

  if (isGenre) {
    const background = `linear-gradient(135deg, ${card.gradient_from ?? "#475569"}, ${card.gradient_to ?? "#0f172a"})`;
    return (
      <button
        type="button"
        onClick={handleClick}
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
      </button>
    );
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      aria-label={card.display_name}
      className={cn(
        baseClasses,
        "bg-gray-800 ring-gray-700 hover:bg-gray-700 hover:ring-gray-500 focus:ring-2 focus:ring-white",
      )}
    >
      {card.logo_url ? (
        <img
          src={card.logo_url}
          alt={card.display_name}
          loading="lazy"
          className="h-full w-full object-contain px-6 py-7 sm:px-8 sm:py-8"
        />
      ) : (
        <span className="px-3 text-center text-base leading-tight font-semibold text-white">
          {card.display_name}
        </span>
      )}
    </button>
  );
}
