import { useRef, useState } from "react";
import { motion, useMotionValue, useTransform, type PanInfo } from "framer-motion";
import { Check, Info, Play, Star, X } from "lucide-react";
import type { SwipeCard as SwipeCardType } from "@/hooks/queries/recommendations";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

const sourceLabels: Record<string, string> = {
  continue_watching: "Continue",
  next_up: "Next Up",
  recommendation: "For You",
};

const SWIPE_THRESHOLD = 100;
const SWIPE_VELOCITY = 0.4;

interface SwipeCardProps {
  card: SwipeCardType;
  isTop: boolean;
  onAccept: () => void;
  onReject: () => void;
}

export default function SwipeCard({ card, isTop, onAccept, onReject }: SwipeCardProps) {
  const [isFlipped, setIsFlipped] = useState(false);
  const didDragRef = useRef(false);

  // Motion values for smooth drag-driven transforms.
  const x = useMotionValue(0);
  const rotate = useTransform(x, [-200, 0, 200], [-12, 0, 12]);
  const rejectOpacity = useTransform(x, [-SWIPE_THRESHOLD, 0], [1, 0]);
  const acceptOpacity = useTransform(x, [0, SWIPE_THRESHOLD], [0, 1]);

  const source = card.watch_tonight_source ?? "";
  const isInProgress = source === "continue_watching";
  const hasEpisodeMeta = card.season_number != null && card.episode_number != null;
  const heading = hasEpisodeMeta && card.series_title ? card.series_title : card.title;
  const progressPercent =
    isInProgress && (card.duration_seconds ?? 0) > 0
      ? ((card.position_seconds ?? 0) / (card.duration_seconds ?? 1)) * 100
      : 0;

  const rating = card.rating_imdb ?? card.rating_tmdb;
  const badgeLabel = sourceLabels[source];

  function handleDragEnd(_: unknown, info: PanInfo) {
    const { offset, velocity } = info;
    if (Math.abs(offset.x) > SWIPE_THRESHOLD || Math.abs(velocity.x) > SWIPE_VELOCITY) {
      if (offset.x > 0) onAccept();
      else onReject();
    }
  }

  return (
    <motion.div
      className="absolute inset-0"
      style={isTop ? { x, rotate } : undefined}
      drag={isTop ? "x" : false}
      dragConstraints={{ left: 0, right: 0 }}
      dragElastic={0.9}
      onDragStart={() => {
        didDragRef.current = true;
      }}
      onDragEnd={handleDragEnd}
    >
      <div
        className={cn(
          "relative h-full w-full select-none",
          isTop && "cursor-grab touch-none active:cursor-grabbing",
        )}
        style={{
          transformStyle: "preserve-3d",
          transform: isFlipped ? "rotateY(180deg)" : "rotateY(0deg)",
          transition: "transform 0.45s ease",
        }}
        onClick={(e) => {
          if (!isTop) return;
          if (didDragRef.current) {
            didDragRef.current = false;
            return;
          }
          if ((e.target as HTMLElement).closest("[data-action]") === null) {
            setIsFlipped((f) => !f);
          }
        }}
      >
        {/* Front face */}
        <div
          className="absolute inset-0 overflow-hidden rounded-2xl"
          style={{ backfaceVisibility: "hidden", WebkitBackfaceVisibility: "hidden" }}
        >
          {/* Backdrop image */}
          {card.backdrop_url ? (
            <img
              src={card.backdrop_url}
              alt={heading}
              className="h-full w-full object-cover"
              draggable={false}
            />
          ) : card.poster_url ? (
            <img
              src={card.poster_url}
              alt={heading}
              className="h-full w-full object-cover"
              draggable={false}
            />
          ) : (
            <div className="bg-muted flex h-full w-full items-center justify-center rounded-2xl">
              <span className="text-muted-foreground text-sm">No Image</span>
            </div>
          )}

          {/* Gradient overlay */}
          <div className="absolute inset-0 bg-gradient-to-t from-black/80 via-black/20 to-transparent" />

          {/* Source badge */}
          {badgeLabel && (
            <Badge variant="secondary" className="absolute top-3 right-3 text-[10px]">
              {badgeLabel}
            </Badge>
          )}

          {/* Swipe indicators — driven by motion values for smooth response */}
          {isTop && (
            <>
              <motion.div
                className="absolute top-4 left-4 flex h-14 w-14 items-center justify-center rounded-full border-2 border-red-500 text-red-500"
                style={{ opacity: rejectOpacity, rotate: -15 }}
              >
                <X className="h-8 w-8" strokeWidth={3} />
              </motion.div>
              <motion.div
                className="absolute top-4 right-4 flex h-14 w-14 items-center justify-center rounded-full border-2 border-green-500 text-green-500"
                style={{ opacity: acceptOpacity, rotate: 15 }}
              >
                <Check className="h-8 w-8" strokeWidth={3} />
              </motion.div>
            </>
          )}

          {/* Bottom info */}
          <div className="absolute inset-x-0 bottom-0 p-5">
            {card.logo_url ? (
              <img
                src={card.logo_url}
                alt={heading}
                className="mb-2 h-10 max-w-[70%] object-contain object-left"
                draggable={false}
              />
            ) : (
              <h3 className="mb-1 text-xl font-bold text-white drop-shadow-md">{heading}</h3>
            )}

            {hasEpisodeMeta && (
              <p className="mb-1 text-sm text-white/80">
                S{card.season_number} E{card.episode_number}
                {card.title && card.series_title ? ` \u2022 ${card.title}` : ""}
              </p>
            )}

            <div className="flex flex-wrap items-center gap-2">
              {card.year > 0 && (
                <span className="text-sm font-medium text-white/70">{card.year}</span>
              )}
              {rating != null && (
                <span className="flex items-center gap-0.5 text-sm font-medium text-yellow-400">
                  <Star className="h-3.5 w-3.5" fill="currentColor" />
                  {rating.toFixed(1)}
                </span>
              )}
              {card.runtime != null && card.runtime > 0 && (
                <span className="text-sm text-white/60">
                  {card.runtime >= 60
                    ? `${Math.floor(card.runtime / 60)}h ${card.runtime % 60}m`
                    : `${card.runtime}m`}
                </span>
              )}
              {card.genres?.slice(0, 3).map((g) => (
                <Badge
                  key={g}
                  variant="outline"
                  className="border-white/30 text-[10px] text-white/70"
                >
                  {g}
                </Badge>
              ))}
            </div>
          </div>

          {/* Progress bar for continue watching */}
          {isInProgress && progressPercent > 0 && (
            <div className="absolute inset-x-0 bottom-0 h-1 bg-white/20">
              <div
                className="h-full"
                style={{
                  width: `${Math.min(progressPercent, 100)}%`,
                  background: "var(--primary)",
                }}
              />
            </div>
          )}

          {/* Flip hint */}
          {isTop && (
            <div className="absolute right-3 bottom-3 flex items-center gap-1 rounded-full bg-black/40 px-2 py-1 text-[10px] text-white/60 backdrop-blur-sm">
              <Info className="h-3 w-3" />
              Tap for details
            </div>
          )}
        </div>

        {/* Back face */}
        <div
          className="absolute inset-0 overflow-hidden rounded-2xl"
          style={{
            backfaceVisibility: "hidden",
            WebkitBackfaceVisibility: "hidden",
            transform: "rotateY(180deg)",
          }}
        >
          {/* Blurred backdrop */}
          {card.backdrop_url && (
            <img
              src={card.backdrop_url}
              alt=""
              className="absolute inset-0 h-full w-full scale-110 object-cover blur-xl brightness-[0.3]"
              draggable={false}
            />
          )}
          <div className="bg-background/80 absolute inset-0" />

          <div className="overlay-scroll relative flex h-full flex-col gap-4 overflow-y-auto p-5">
            <h3 className="text-lg font-bold">{heading}</h3>

            {hasEpisodeMeta && (
              <p className="text-muted-foreground text-sm">
                S{card.season_number} E{card.episode_number}
                {card.title && card.series_title ? ` \u2022 ${card.title}` : ""}
              </p>
            )}

            {card.overview && (
              <p className="text-muted-foreground line-clamp-6 text-sm leading-relaxed">
                {card.overview}
              </p>
            )}

            {card.cast?.length > 0 && (
              <div>
                <h4 className="text-muted-foreground mb-1.5 text-xs font-semibold tracking-wider uppercase">
                  Cast
                </h4>
                <div className="space-y-1">
                  {card.cast.map((c) => (
                    <p key={c.name} className="text-sm">
                      <span className="font-medium">{c.name}</span>
                      {c.character && (
                        <span className="text-muted-foreground"> as {c.character}</span>
                      )}
                    </p>
                  ))}
                </div>
              </div>
            )}

            <div className="flex flex-wrap gap-3 text-sm">
              {rating != null && (
                <span className="flex items-center gap-1 text-yellow-400">
                  <Star className="h-3.5 w-3.5" fill="currentColor" />
                  {rating.toFixed(1)}
                </span>
              )}
              {card.runtime != null && card.runtime > 0 && (
                <span className="text-muted-foreground">
                  {card.runtime >= 60
                    ? `${Math.floor(card.runtime / 60)}h ${card.runtime % 60}m`
                    : `${card.runtime}m`}
                </span>
              )}
              {card.year > 0 && <span className="text-muted-foreground">{card.year}</span>}
            </div>

            {card.genres && card.genres.length > 0 && (
              <div className="flex flex-wrap gap-1.5">
                {card.genres.map((g) => (
                  <Badge key={g} variant="outline" className="text-[10px]">
                    {g}
                  </Badge>
                ))}
              </div>
            )}
          </div>

          {/* Flip back hint */}
          <div className="absolute right-3 bottom-3 flex items-center gap-1 rounded-full bg-black/40 px-2 py-1 text-[10px] text-white/60 backdrop-blur-sm">
            <Info className="h-3 w-3" />
            Tap to flip back
          </div>
        </div>
      </div>
    </motion.div>
  );
}

// --- Action buttons shown below the card stack ---

interface CardActionsProps {
  onReject: () => void;
  onAccept: () => void;
  onPlay: () => void;
  playLabel?: string;
  disabled?: boolean;
}

export function CardActions({
  onReject,
  onAccept,
  onPlay,
  playLabel = "Play now",
  disabled,
}: CardActionsProps) {
  return (
    <div className="flex items-center justify-center gap-4 pt-4">
      <button
        type="button"
        data-action
        onClick={onReject}
        disabled={disabled}
        className={cn(
          "flex h-14 w-14 items-center justify-center rounded-full border-2 transition-colors",
          "border-red-500/40 text-red-400 hover:border-red-500 hover:bg-red-500/10 hover:text-red-500",
          "disabled:pointer-events-none disabled:opacity-40",
        )}
        aria-label="Skip"
      >
        <X className="h-6 w-6" />
      </button>
      <button
        type="button"
        data-action
        onClick={onPlay}
        disabled={disabled}
        className={cn(
          "flex h-16 w-16 items-center justify-center rounded-full border-2 transition-colors",
          "border-primary/40 text-primary hover:border-primary hover:bg-primary/10",
          "disabled:pointer-events-none disabled:opacity-40",
        )}
        aria-label={playLabel}
      >
        <Play className="ml-0.5 h-7 w-7" fill="currentColor" />
      </button>
      <button
        type="button"
        data-action
        onClick={onAccept}
        disabled={disabled}
        className={cn(
          "flex h-14 w-14 items-center justify-center rounded-full border-2 transition-colors",
          "border-green-500/40 text-green-400 hover:border-green-500 hover:bg-green-500/10 hover:text-green-500",
          "disabled:pointer-events-none disabled:opacity-40",
        )}
        aria-label="Like"
      >
        <Check className="h-6 w-6" />
      </button>
    </div>
  );
}
