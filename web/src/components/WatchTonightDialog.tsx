import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowRight, History, Sparkles } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { type SwipeMode, useSwipeCards } from "@/hooks/queries/recommendations";
import GenrePicker from "./watchtonight/GenrePicker";
import CardStack from "./watchtonight/CardStack";

type Step = "mode-select" | "genre-picker" | "swipe-deck";

interface WatchTonightDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export default function WatchTonightDialog({ open, onOpenChange }: WatchTonightDialogProps) {
  const [step, setStep] = useState<Step>("mode-select");
  const [mode, setMode] = useState<SwipeMode>("discover");
  const [genres, setGenres] = useState<string[]>([]);

  // Reset state when dialog closes.
  useEffect(() => {
    if (!open) {
      // Small delay to let close animation finish before resetting.
      const t = setTimeout(() => {
        setStep("mode-select");
        setMode("discover");
        setGenres([]);
      }, 200);
      return () => clearTimeout(t);
    }
  }, [open]);

  const swipeEnabled = open && step === "swipe-deck";
  const { data, fetchNextPage, hasNextPage, isFetching, isFetchingNextPage } = useSwipeCards(
    swipeEnabled,
    mode,
    genres,
  );

  const cards = useMemo(() => data?.pages.flatMap((p) => p.cards) ?? [], [data]);

  const hasMore = hasNextPage ?? false;

  const handleModeSelect = useCallback((selectedMode: SwipeMode) => {
    setMode(selectedMode);
    if (selectedMode === "continue") {
      setStep("swipe-deck");
    } else {
      setStep("genre-picker");
    }
  }, []);

  const handleGenresConfirm = useCallback(() => {
    setStep("swipe-deck");
  }, []);

  const handleNeedMore = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);

  const handleClose = useCallback(() => {
    onOpenChange(false);
  }, [onOpenChange]);

  const handleReset = useCallback(() => {
    setStep("mode-select");
    setGenres([]);
  }, []);

  // Dynamic dialog sizing based on step.
  const dialogClass =
    step === "swipe-deck"
      ? "gap-0 overflow-hidden p-0 sm:max-w-md"
      : "gap-0 overflow-hidden p-0 sm:max-w-lg";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={dialogClass}>
        <DialogHeader className="px-6 pt-6 pb-4">
          <DialogTitle className="flex items-center gap-2">
            <Sparkles className="text-primary h-5 w-5" />
            Watch Tonight
          </DialogTitle>
          <DialogDescription>
            {step === "mode-select" && "What are you in the mood for?"}
            {step === "genre-picker" && "Pick some genres to narrow things down"}
            {step === "swipe-deck" &&
              (mode === "continue"
                ? "Swipe through your in-progress titles"
                : "Swipe right to play, left to skip")}
          </DialogDescription>
        </DialogHeader>

        <div className="px-6 pb-6">
          {/* Step 1: Mode Selection */}
          {step === "mode-select" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <button
                type="button"
                onClick={() => handleModeSelect("continue")}
                className="group border-border hover:border-primary/50 hover:bg-primary/5 flex flex-col items-center gap-3 rounded-xl border-2 p-6 text-center transition-all"
              >
                <div className="bg-primary/10 text-primary flex h-12 w-12 items-center justify-center rounded-full">
                  <History className="h-6 w-6" />
                </div>
                <div>
                  <p className="font-semibold">Pick Up Where I Left Off</p>
                  <p className="text-muted-foreground mt-1 text-xs">
                    Continue in-progress titles or start what is next
                  </p>
                </div>
              </button>

              <button
                type="button"
                onClick={() => handleModeSelect("discover")}
                className="group border-border hover:border-primary/50 hover:bg-primary/5 flex flex-col items-center gap-3 rounded-xl border-2 p-6 text-center transition-all"
              >
                <div className="bg-primary/10 text-primary flex h-12 w-12 items-center justify-center rounded-full">
                  <Sparkles className="h-6 w-6" />
                </div>
                <div>
                  <p className="font-semibold">Find Something New</p>
                  <p className="text-muted-foreground mt-1 text-xs">
                    Discover personalized recommendations
                  </p>
                </div>
              </button>
            </div>
          )}

          {/* Step 2: Genre Picker */}
          {step === "genre-picker" && (
            <div className="space-y-5">
              <GenrePicker selected={genres} onChange={setGenres} />
              <div className="flex items-center justify-between">
                <button
                  type="button"
                  onClick={() => setStep("mode-select")}
                  className="text-muted-foreground text-sm hover:underline"
                >
                  Back
                </button>
                <Button onClick={handleGenresConfirm} size="sm" className="gap-1.5">
                  {genres.length === 0 ? "All Genres" : `Go (${genres.length})`}
                  <ArrowRight className="h-4 w-4" />
                </Button>
              </div>
            </div>
          )}

          {/* Step 3: Swipe Deck */}
          {step === "swipe-deck" && (
            <CardStack
              cards={cards}
              hasMore={hasMore}
              isFetching={isFetching || isFetchingNextPage}
              onNeedMore={handleNeedMore}
              onClose={handleClose}
              onReset={handleReset}
            />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
