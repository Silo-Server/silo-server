import { useCallback, useEffect, useRef, useState } from "react";
import { AnimatePresence, motion } from "framer-motion";
import { useLocation, useNavigate } from "react-router";
import { RefreshCw, Tv } from "lucide-react";
import type { SwipeCard as SwipeCardType } from "@/hooks/queries/recommendations";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { Skeleton } from "@/components/ui/skeleton";
import SwipeCard, { CardActions } from "./SwipeCard";

// Stack positioning for each depth level (0 = top, 1 = behind, 2 = further).
const stackVariants = {
  0: { scale: 1, y: 0, opacity: 1 },
  1: { scale: 0.95, y: 8, opacity: 0.7 },
  2: { scale: 0.9, y: 16, opacity: 0.4 },
};

const stackTransition = { type: "spring" as const, stiffness: 400, damping: 30 };

interface CardStackProps {
  cards: SwipeCardType[];
  hasMore: boolean;
  isFetching: boolean;
  onNeedMore: () => void;
  onClose: () => void;
  onReset: () => void;
}

export default function CardStack({
  cards,
  hasMore,
  isFetching,
  onNeedMore,
  onClose,
  onReset,
}: CardStackProps) {
  const [topIndex, setTopIndex] = useState(0);
  const prefetchTriggered = useRef(false);
  const location = useLocation();
  const navigate = useNavigate();
  const playbackController = useWatchPlaybackController();

  const visibleCards = cards.slice(topIndex, topIndex + 3);
  const isDone = visibleCards.length === 0 && !hasMore && !isFetching;

  // Prefetch next page when 3 cards remain.
  useEffect(() => {
    const remaining = cards.length - topIndex;
    if (remaining <= 3 && hasMore && !isFetching && !prefetchTriggered.current) {
      prefetchTriggered.current = true;
      onNeedMore();
    }
    if (remaining > 3) {
      prefetchTriggered.current = false;
    }
  }, [topIndex, cards.length, hasMore, isFetching, onNeedMore]);

  const advance = useCallback(() => {
    setTopIndex((i) => i + 1);
  }, []);

  const handlePlay = useCallback(() => {
    const card = cards[topIndex];
    if (!card) return;
    onClose();
    if (card.type === "ebook") {
      navigate(`/reader/ebook/${encodeURIComponent(card.content_id)}`);
      return;
    }
    playbackController.startPlayback({
      contentId: card.content_id,
      returnHref: `${location.pathname}${location.search}`,
    });
  }, [cards, topIndex, onClose, navigate, playbackController, location.pathname, location.search]);

  // Keyboard navigation.
  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if (visibleCards.length === 0) return;
      if (e.key === "ArrowLeft") {
        e.preventDefault();
        advance();
      } else if (e.key === "ArrowRight") {
        e.preventDefault();
        handlePlay();
      }
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [visibleCards.length, advance, handlePlay]);

  // Loading initial cards.
  if (cards.length === 0 && isFetching) {
    return (
      <div className="flex flex-col items-center gap-4 py-8">
        <div className="relative h-80 w-full max-w-sm">
          <Skeleton className="h-full w-full rounded-2xl" />
        </div>
      </div>
    );
  }

  // All done — no more cards.
  if (isDone) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-12 text-center">
        <Tv className="text-muted-foreground h-12 w-12" />
        <p className="text-muted-foreground text-sm">
          {"You've seen everything! Come back later for fresh picks."}
        </p>
        <button
          type="button"
          onClick={onReset}
          className="border-border hover:bg-muted/40 flex items-center gap-2 rounded-lg border px-4 py-2 text-sm font-medium"
        >
          <RefreshCw className="h-4 w-4" />
          Start Over
        </button>
      </div>
    );
  }

  // Waiting for more cards (current batch exhausted but more available).
  if (visibleCards.length === 0 && isFetching) {
    return (
      <div className="flex flex-col items-center gap-4 py-8">
        <div className="relative h-80 w-full max-w-sm">
          <Skeleton className="h-full w-full rounded-2xl" />
        </div>
        <p className="text-muted-foreground text-sm">Loading more picks...</p>
      </div>
    );
  }

  return (
    <div className="flex flex-col items-center gap-2">
      {/* Card stack area */}
      <div className="relative h-80 w-full max-w-sm sm:h-96">
        <AnimatePresence mode="popLayout">
          {/* Render in reverse so the top card is last in DOM (highest z-index) */}
          {[...visibleCards].reverse().map((card, reverseIdx) => {
            const depth = visibleCards.length - 1 - reverseIdx; // 0 = top
            const isTop = depth === 0;

            return (
              <motion.div
                key={card.content_id}
                className="absolute inset-0"
                style={{ zIndex: visibleCards.length - depth }}
                // Animate into stack position when promoted.
                initial={stackVariants[2]}
                animate={stackVariants[Math.min(depth, 2) as 0 | 1 | 2]}
                // Exit: fly off to the side and shrink.
                exit={{
                  x: -300,
                  opacity: 0,
                  scale: 0.8,
                  rotate: -10,
                  transition: { duration: 0.3, ease: "easeIn" },
                }}
                transition={stackTransition}
              >
                <SwipeCard card={card} isTop={isTop} onAccept={handlePlay} onReject={advance} />
              </motion.div>
            );
          })}
        </AnimatePresence>
      </div>

      {/* Action buttons */}
      {visibleCards.length > 0 && (
        <CardActions onReject={advance} onAccept={handlePlay} onPlay={handlePlay} />
      )}

      {/* Counter */}
      <p className="text-muted-foreground pt-1 text-xs">
        {topIndex + 1} / {hasMore ? `${cards.length}+` : cards.length}
      </p>
    </div>
  );
}
