import { useImageLoaded } from "@/hooks/useImageLoaded";
import { Pin, PinOff, User } from "lucide-react";
import type { LibraryTabCollection } from "@/api/types";
import { useToggleSidebarPin } from "@/hooks/queries/sidebarPins";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import {
  buildLibraryCollectionCatalogHref,
  buildUserCollectionCatalogHref,
} from "@/pages/catalogSearchParams";

// Shared poster card for both the per-library Collections tab and the
// aggregated "Server collections" section on the home Collections tab. Library
// (admin) collections get a sidebar-pin affordance; user collections do not.
export const COLLECTION_POSTER_GRID_CLASSES =
  "grid grid-cols-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8 gap-3";

export function CollectionPosterCard({
  collection,
  kind,
  libraryId,
}: {
  collection: LibraryTabCollection;
  kind: "regular" | "user_collections";
  libraryId: number;
}) {
  const { loaded, onLoad } = useImageLoaded(collection.poster_url);
  const navigate = useViewTransitionNavigate();
  const { togglePin, isPinned } = useToggleSidebarPin();
  const pinned = isPinned(libraryId, "collection", collection.id);

  const isUserCollection = kind === "user_collections";
  const href = isUserCollection
    ? buildUserCollectionCatalogHref(collection.id, collection.title)
    : buildLibraryCollectionCatalogHref(collection.id, collection.title);

  return (
    <div className="group/card relative w-full text-left">
      <button type="button" onClick={() => navigate(href)} className="block w-full text-left">
        <div className="media-card-image relative aspect-[2/3] overflow-hidden rounded-xl">
          {collection.poster_url ? (
            <img
              src={collection.poster_url}
              alt={collection.title}
              className={`h-full w-full object-cover transition duration-300 group-hover/card:scale-[1.05] ${
                loaded ? "opacity-100" : "opacity-0"
              }`}
              loading="lazy"
              onLoad={onLoad}
            />
          ) : (
            <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-sm">
              {isUserCollection && <User className="h-5 w-5 opacity-60" />}
              <span className="line-clamp-3 font-medium">{collection.title}</span>
            </div>
          )}
          <span className="bg-background/60 text-foreground absolute right-2 bottom-2 rounded-md px-2 py-0.5 text-[11px] font-bold backdrop-blur-sm">
            {collection.item_count}
          </span>
        </div>
        <div className="px-0.5 pt-2.5">
          <div className="truncate text-[13px] font-semibold">{collection.title}</div>
          {isUserCollection && <div className="text-muted-foreground text-xs">User collection</div>}
        </div>
      </button>
      {/* Pin to sidebar button — only for library collections */}
      {!isUserCollection && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            togglePin(libraryId, {
              type: "collection",
              id: collection.id,
              label: collection.title,
            });
          }}
          className={`absolute top-2 left-2 rounded-lg p-1.5 backdrop-blur-sm transition-all ${
            pinned
              ? "bg-primary/90 text-primary-foreground"
              : "bg-background/50 text-foreground opacity-0 group-hover/card:opacity-100"
          }`}
          title={pinned ? "Unpin from sidebar" : "Pin to sidebar"}
        >
          {pinned ? <PinOff className="h-3.5 w-3.5" /> : <Pin className="h-3.5 w-3.5" />}
        </button>
      )}
    </div>
  );
}
