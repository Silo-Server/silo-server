import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { Check, Loader2, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/useAuth";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { USER_CAPABILITY_ACTIONS, useUserCapabilityAccess } from "@/hooks/useUserCapabilities";
import { decodeThumbhash } from "@/lib/thumbhash";
import type { SectionItem } from "@/api/types";
import { useTasteSeedItems, useSubmitTasteSeed } from "@/hooks/queries/tasteSeed";
import { setTasteSeedDismissed } from "@/lib/tasteSeed";

const MIN_PICKS = 3;

export default function TasteSeed() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { profile } = useAuth();
  const userCapabilities = useUserCapabilityAccess();
  const canManagePersonalLists = userCapabilities.can(USER_CAPABILITY_ACTIONS.personalListsManage);
  useDocumentTitle("Pick what you love");

  const isReturning = searchParams.get("from") === "settings";

  // Track only the user's explicit toggles. Final `selected` set is derived
  // from items + toggles each render, so already-favorited items are
  // automatically pre-selected without needing setState-in-effect.
  const [userToggles, setUserToggles] = useState<Map<string, boolean>>(new Map());

  const { data, fetchNextPage, hasNextPage, isFetching, isFetchingNextPage, isPending } =
    useTasteSeedItems(canManagePersonalLists);
  const submit = useSubmitTasteSeed();

  useEffect(() => {
    if (userCapabilities.isLoading || canManagePersonalLists) return;
    if (profile) {
      setTasteSeedDismissed(profile.id);
    }
    navigate(isReturning ? "/settings/playback" : "/", { replace: true });
  }, [canManagePersonalLists, isReturning, navigate, profile, userCapabilities.isLoading]);

  // Flatten paginated pages into a single item list.
  const items = useMemo(() => data?.pages.flatMap((p) => p.items) ?? [], [data]);

  // Derive the selected set: items already favorited are selected unless the
  // user explicitly deselected them; user-added picks are also selected.
  const selected = useMemo(() => {
    const set = new Set<string>();
    for (const item of items) {
      const toggle = userToggles.get(item.content_id);
      if (toggle === true) {
        set.add(item.content_id);
      } else if (toggle === false) {
        // user explicitly deselected — leave out
      } else if (item.user_state?.is_favorite) {
        set.add(item.content_id);
      }
    }
    return set;
  }, [items, userToggles]);

  // Infinite-scroll trigger: load the next page when sentinel becomes visible.
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const sentinel = sentinelRef.current;
    if (!sentinel || !hasNextPage || isFetchingNextPage) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) {
          void fetchNextPage();
        }
      },
      { rootMargin: "400px 0px" },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);

  const toggle = useCallback(
    (contentId: string) => {
      setUserToggles((prev) => {
        const next = new Map(prev);
        const currentlySelected = selected.has(contentId);
        next.set(contentId, !currentlySelected);
        return next;
      });
    },
    [selected],
  );

  const handleSkip = useCallback(() => {
    if (profile) {
      setTasteSeedDismissed(profile.id);
    }
    navigate(isReturning ? "/settings/playback" : "/", { replace: true });
  }, [navigate, profile, isReturning]);

  const handleSubmit = useCallback(async () => {
    if (!canManagePersonalLists) return;
    if (selected.size < MIN_PICKS) {
      toast.error(`Pick at least ${MIN_PICKS} to continue`);
      return;
    }
    // Only submit IDs that aren't already favorited — the server will skip
    // duplicates anyway, but trimming the request keeps it small.
    const alreadyFavorited = new Set(
      items.filter((i) => i.user_state?.is_favorite).map((i) => i.content_id),
    );
    const newPicks = Array.from(selected).filter((id) => !alreadyFavorited.has(id));

    if (newPicks.length === 0) {
      // All selected items are already favorited — no work to do, but still
      // record the dismissed flag so future logins skip the redirect.
      if (profile) {
        setTasteSeedDismissed(profile.id);
      }
      navigate(isReturning ? "/settings/playback" : "/", { replace: true });
      return;
    }

    try {
      const result = await submit.mutateAsync(newPicks);
      if (profile) {
        setTasteSeedDismissed(profile.id);
      }
      toast.success(
        result.added === 1
          ? "Added 1 favorite — personalizing your home"
          : `Added ${result.added} favorites — personalizing your home`,
      );
      navigate(isReturning ? "/settings/playback" : "/", { replace: true });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save your picks");
    }
  }, [canManagePersonalLists, selected, items, submit, navigate, profile, isReturning]);

  const submitDisabled = !canManagePersonalLists || selected.size < MIN_PICKS || submit.isPending;

  return (
    <div className="min-h-[100dvh]">
      {/* Sticky header with progress + actions */}
      <header className="bg-background/80 supports-[backdrop-filter]:bg-background/60 border-border sticky top-0 z-30 border-b backdrop-blur">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-3 px-4 py-4 sm:px-6">
          <div className="min-w-0 flex-1">
            <h1 className="flex items-center gap-2 text-xl font-bold tracking-tight sm:text-2xl">
              <Sparkles className="text-primary h-5 w-5" aria-hidden="true" />
              Pick what you love
            </h1>
            <p className="text-muted-foreground mt-0.5 text-sm">
              {selected.size === 0
                ? `Select at least ${MIN_PICKS} titles to personalize your recommendations`
                : `${selected.size} selected${selected.size < MIN_PICKS ? ` — ${MIN_PICKS - selected.size} more to continue` : ""}`}
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button variant="ghost" onClick={handleSkip} disabled={submit.isPending}>
              {isReturning ? "Cancel" : "Skip"}
            </Button>
            <Button onClick={handleSubmit} disabled={submitDisabled}>
              {submit.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Saving...
                </>
              ) : (
                "Done"
              )}
            </Button>
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-6xl px-4 py-6 sm:px-6 sm:py-8">
        {isPending ? (
          <TasteSeedGridSkeleton />
        ) : items.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-3 py-24 text-center">
            <p className="text-base font-medium">No items to show yet</p>
            <p className="text-sm">
              Once your library has matched content, you'll see popular titles here.
            </p>
            <Button variant="ghost" className="mt-4" onClick={handleSkip}>
              Continue
            </Button>
          </div>
        ) : (
          <>
            <div
              role="listbox"
              aria-multiselectable="true"
              aria-label="Popular titles to choose from"
              className="grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7"
            >
              {items.map((item) => (
                <TasteSeedCard
                  key={item.content_id}
                  item={item}
                  selected={selected.has(item.content_id)}
                  onToggle={toggle}
                />
              ))}
            </div>
            {/* Infinite-scroll sentinel */}
            <div ref={sentinelRef} className="h-12" />
            {isFetchingNextPage && (
              <div className="text-muted-foreground flex justify-center py-4 text-sm">
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                Loading more
              </div>
            )}
            {!hasNextPage && !isFetching && items.length > 0 && (
              <p className="text-muted-foreground py-6 text-center text-xs">
                That's everything popular on this server.
              </p>
            )}
          </>
        )}
      </main>
    </div>
  );
}

function TasteSeedCard({
  item,
  selected,
  onToggle,
}: {
  item: SectionItem;
  selected: boolean;
  onToggle: (contentId: string) => void;
}) {
  const [loaded, setLoaded] = useState(false);
  const thumbhashUrl = item.poster_thumbhash ? decodeThumbhash(item.poster_thumbhash) : "";

  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      aria-label={selected ? `Deselect ${item.title}` : `Select ${item.title}`}
      onClick={() => onToggle(item.content_id)}
      className="group focus-visible:ring-ring relative block overflow-hidden rounded-xl text-left focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:outline-none"
    >
      <div
        className="relative aspect-[2/3] overflow-hidden rounded-xl"
        style={
          thumbhashUrl
            ? {
                backgroundImage: `url(${thumbhashUrl})`,
                backgroundSize: "cover",
                backgroundPosition: "center",
              }
            : undefined
        }
      >
        {item.poster_url ? (
          <img
            src={item.poster_url}
            alt=""
            className={`h-full w-full object-cover transition-opacity duration-300 ${loaded ? "opacity-100" : "opacity-0"}`}
            loading="lazy"
            onLoad={() => setLoaded(true)}
          />
        ) : (
          <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-xs">
            <span className="line-clamp-3 font-medium">{item.title || "No Poster"}</span>
          </div>
        )}

        {/* Dim overlay when unselected to make selected posters pop */}
        <div
          className={`pointer-events-none absolute inset-0 transition-opacity ${
            selected ? "bg-primary/20 opacity-100" : "bg-black/0 group-hover:bg-black/15"
          }`}
        />

        {/* Selection check indicator */}
        <span
          className={`absolute top-2 right-2 inline-flex size-7 items-center justify-center rounded-full border transition ${
            selected
              ? "border-primary bg-primary text-primary-foreground"
              : "border-white/60 bg-black/40 text-transparent group-hover:bg-black/55"
          }`}
        >
          <Check className="size-4" />
        </span>
      </div>
    </button>
  );
}

function TasteSeedGridSkeleton() {
  return (
    <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7">
      {Array.from({ length: 24 }).map((_, i) => (
        <div key={i} className="bg-muted/40 aspect-[2/3] animate-pulse rounded-xl" />
      ))}
    </div>
  );
}
