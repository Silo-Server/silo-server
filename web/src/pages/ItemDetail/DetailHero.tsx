import { type ReactNode, useState } from "react";
import { decodeThumbhash } from "@/lib/thumbhash";

interface DetailHeroProps {
  title: string;
  subtitle?: ReactNode;
  context?: ReactNode;
  backdropUrl?: string;
  backdropThumbhash?: string;
  posterUrl?: string;
  posterThumbhash?: string;
  posterOrientation?: "portrait" | "landscape";
  hidePoster?: boolean;
  logoUrl?: string;
  metadata?: ReactNode;
  genres?: string[];
  overview?: string;
  actions?: ReactNode;
  aside?: ReactNode;
  studioLabel?: string;
  tagline?: string;
  scoreRow?: ReactNode;
  crewLine?: ReactNode;
  variant?: "full" | "compact";
  topNav?: ReactNode;
}

export default function DetailHero({
  title,
  subtitle,
  context,
  backdropUrl,
  backdropThumbhash,
  posterUrl,
  posterThumbhash,
  posterOrientation = "portrait",
  hidePoster = false,
  logoUrl,
  metadata,
  genres,
  overview,
  actions,
  aside,
  studioLabel,
  tagline,
  scoreRow,
  crewLine,
  variant = "full",
  topNav,
}: DetailHeroProps) {
  const [backdropLoaded, setBackdropLoaded] = useState(false);
  const [posterLoaded, setPosterLoaded] = useState(false);
  const backdropPlaceholder = backdropThumbhash ? decodeThumbhash(backdropThumbhash) : "";
  const posterPlaceholder = posterThumbhash ? decodeThumbhash(posterThumbhash) : "";
  const isCompact = variant === "compact";

  const posterSizeClass =
    posterOrientation === "portrait"
      ? isCompact
        ? "w-[140px] sm:w-[160px]"
        : "w-[170px] sm:w-[220px]"
      : isCompact
        ? "w-[200px] sm:w-[260px]"
        : "w-[240px] sm:w-[320px]";

  const posterAspect = posterOrientation === "portrait" ? "aspect-[2/3]" : "aspect-video";

  return (
    <section className="item-detail-hero border-border/10 relative isolate overflow-hidden border-b">
      {topNav}
      {(backdropUrl || backdropPlaceholder) && (
        <div
          className="absolute inset-0 h-full w-full"
          style={{
            filter: `brightness(var(--hero-backdrop-brightness, 0.4)) saturate(var(--hero-backdrop-saturate, 1.15))`,
            ...(backdropPlaceholder && !backdropLoaded
              ? {
                  backgroundImage: `url(${backdropPlaceholder})`,
                  backgroundSize: "cover",
                  backgroundPosition: "center 20%",
                }
              : undefined),
          }}
        >
          {backdropUrl && (
            <img
              key={backdropUrl}
              src={backdropUrl}
              alt=""
              className={`h-full w-full object-cover object-[center_20%] transition-opacity duration-300 will-change-transform ${backdropLoaded ? "opacity-100" : "opacity-0"}`}
              style={{ animation: "var(--animate-ken-burns-a)" }}
              onLoad={() => setBackdropLoaded(true)}
            />
          )}
        </div>
      )}

      {/* Left-to-right gradient */}
      <div className="hero-gradient-left" />

      {/* Ambient glow from artwork */}
      <div className="ambient-glow" />

      {/* Bottom-to-top gradient */}
      <div className="hero-gradient" />
      <div className="hero-vignette" />

      <div
        className={`page-shell-wide relative flex flex-col justify-end pb-8 ${
          isCompact
            ? "h-[35vh] min-h-[300px] pt-20 lg:h-[42vh]"
            : "min-h-[60dvh] pt-28 lg:min-h-[72dvh]"
        }`}
      >
        <div
          className={`grid gap-8 ${
            !isCompact && aside ? "lg:grid-cols-[minmax(0,1fr)_280px] lg:items-end" : ""
          }`}
        >
          <div className={`flex flex-col gap-6 ${!hidePoster ? "lg:flex-row lg:items-end" : ""}`}>
            {/* Poster */}
            {!hidePoster && (
              <div
                className={`media-card-image border-border/20 border shadow-[var(--shadow-md)] ${posterSizeClass}`}
                style={
                  posterPlaceholder && !posterLoaded
                    ? {
                        backgroundImage: `url(${posterPlaceholder})`,
                        backgroundSize: "cover",
                        backgroundPosition: "center",
                      }
                    : undefined
                }
              >
                {posterUrl ? (
                  <img
                    key={posterUrl}
                    src={posterUrl}
                    alt={title}
                    className={`w-full object-cover ${posterAspect} transition-opacity duration-300 ${posterLoaded ? "opacity-100" : "opacity-0"}`}
                    onLoad={() => setPosterLoaded(true)}
                  />
                ) : (
                  <div
                    className={`text-muted-foreground bg-surface flex items-center justify-center p-6 text-center text-sm ${posterAspect}`}
                  >
                    {title}
                  </div>
                )}
              </div>
            )}

            {/* Info column */}
            <div
              className="max-w-3xl"
              style={{ textShadow: "var(--hero-text-shadow, 0 1px 3px rgb(0 0 0 / 40%))" }}
            >
              {context && (
                <div className="text-muted-foreground mb-4 text-sm font-medium">{context}</div>
              )}

              {studioLabel && (
                <div className="text-muted-foreground mb-2 text-xs font-semibold tracking-[0.16em] uppercase">
                  {studioLabel}
                </div>
              )}

              {logoUrl ? (
                <>
                  <h1 className="sr-only">{title}</h1>
                  <img
                    src={logoUrl}
                    alt=""
                    className="mb-4 max-h-20 max-w-[420px] object-contain object-left lg:max-h-28 lg:max-w-[480px]"
                  />
                </>
              ) : (
                <h1
                  className={`text-foreground mb-3 font-extrabold tracking-tight ${
                    isCompact
                      ? "font-display text-3xl leading-[1.1] sm:text-4xl"
                      : "font-display text-4xl leading-[0.98] tracking-[-0.05em] sm:text-5xl lg:text-7xl"
                  }`}
                >
                  {title}
                </h1>
              )}

              {/* Tagline (italic) — falls back to subtitle */}
              {(tagline || subtitle) && (
                <div
                  className={`text-muted-foreground mb-4 text-[13px] ${
                    tagline
                      ? "text-foreground/72 italic"
                      : "text-muted-foreground text-base font-medium not-italic"
                  }`}
                >
                  {tagline || subtitle}
                </div>
              )}

              {metadata && <div className="mb-4">{metadata}</div>}

              {scoreRow && <div className="mb-4">{scoreRow}</div>}

              {overview && (
                <p
                  className={`text-muted-foreground max-w-2xl leading-7 ${
                    isCompact ? "text-sm" : "text-foreground/72 text-sm sm:text-[15px]"
                  }`}
                >
                  {overview}
                </p>
              )}

              {/* Crew line replaces genres when provided */}
              {crewLine ? (
                <div className="mt-3">{crewLine}</div>
              ) : (
                genres &&
                genres.length > 0 && (
                  <div className="mt-4 flex flex-wrap gap-2">
                    {genres.map((genre) => (
                      <span key={genre} className="metadata-badge">
                        {genre}
                      </span>
                    ))}
                  </div>
                )
              )}

              {actions && (
                <div className="mt-6" style={{ textShadow: "none" }}>
                  {actions}
                </div>
              )}
            </div>
          </div>

          {!isCompact && aside && <div className="lg:justify-self-end">{aside}</div>}
        </div>
      </div>
    </section>
  );
}
