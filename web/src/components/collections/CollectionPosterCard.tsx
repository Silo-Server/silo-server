import type { ReactNode } from "react";
import { useImageLoaded } from "@/hooks/useImageLoaded";
import { Pin, PinOff, User } from "lucide-react";
import type { LibraryTabCollection } from "@/api/types";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useToggleSidebarPin } from "@/hooks/queries/sidebarPins";
import {
  buildLibraryCollectionCatalogHref,
  buildUserCollectionCatalogHref,
} from "@/pages/catalogSearchParams";
import { useUICustomization } from "@/hooks/useUICustomization";

// Shared poster card for the per-library Collections tab and every section of
// the Collections page. Library (admin) collections get a sidebar-pin
// affordance; user collections do not.
export function CollectionPosterCard({
  collection,
  kind,
  libraryId,
  ownerName,
  meta,
  tag,
  menu,
  handle,
}: {
  collection: LibraryTabCollection;
  kind: "regular" | "user_collections";
  /** The library a server collection opens in; unused for user collections. */
  libraryId?: number;
  /** Set on another profile's shared collection: shown as "by Name". */
  ownerName?: string;
  /** The line under the name. When set, the caption always shows and the poster drops its count. */
  meta?: ReactNode;
  /** One tag under the meta line (Shared, say). */
  tag?: ReactNode;
  /** A ⋯ menu over the poster's top-right corner, always visible. */
  menu?: ReactNode;
  /** A drag handle over the poster's top-left corner. */
  handle?: ReactNode;
}) {
  const { loaded, onLoad, onError } = useImageLoaded(collection.poster_url);
  const { cardPresentation } = useUICustomization();

  const isUserCollection = kind === "user_collections";
  const href = isUserCollection
    ? buildUserCollectionCatalogHref(collection.id, collection.title)
    : buildLibraryCollectionCatalogHref(collection.id, collection.title, libraryId);
  const showCaption = meta !== undefined || cardPresentation.caption !== "artwork";
  // A card with its own meta line draws it below the link instead.
  let subline: ReactNode = null;
  if (meta === undefined && ownerName) {
    subline = <div className="text-muted-foreground truncate text-xs">by {ownerName}</div>;
  } else if (
    meta === undefined &&
    cardPresentation.caption === "title_metadata" &&
    isUserCollection
  ) {
    subline = <div className="text-muted-foreground text-xs">User collection</div>;
  }

  return (
    <div className="group/card relative w-full text-left">
      <ViewTransitionLink to={href} className="block w-full text-left">
        <div className="media-card-image relative aspect-[2/3] overflow-hidden rounded-xl">
          {collection.poster_url ? (
            <img
              src={collection.poster_url}
              // The caption names the card; without it the poster does.
              alt={showCaption ? "" : collection.title}
              className={`h-full w-full object-cover transition duration-300 group-hover/card:scale-[1.05] ${
                loaded ? "opacity-100" : "opacity-0"
              }`}
              loading="lazy"
              onLoad={onLoad}
              onError={onError}
            />
          ) : (
            <div
              aria-hidden={showCaption}
              className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-sm"
            >
              {isUserCollection && <User className="h-5 w-5 opacity-60" />}
              <span className="line-clamp-3 font-medium">{collection.title}</span>
            </div>
          )}
          {meta === undefined ? (
            <span className="bg-background/60 text-foreground absolute right-2 bottom-2 rounded-md px-2 py-0.5 text-[0.6875rem] font-bold backdrop-blur-sm">
              {collection.item_count}
            </span>
          ) : null}
        </div>
        {showCaption ? (
          <div className="px-0.5 pt-2.5">
            <div className="truncate text-[0.8125rem] font-semibold">{collection.title}</div>
            {subline}
          </div>
        ) : null}
      </ViewTransitionLink>
      {meta !== undefined ? <div className="px-0.5">{meta}</div> : null}
      {tag ? <div className="px-0.5 pt-1.5">{tag}</div> : null}
      {menu ? <div className="absolute top-2 right-2 z-10">{menu}</div> : null}
      {handle ? <div className="absolute top-2 left-2 z-10">{handle}</div> : null}
      {!isUserCollection && libraryId !== undefined ? (
        <SidebarPinButton collection={collection} libraryId={libraryId} />
      ) : null}
    </div>
  );
}

/** Pins a library collection to the sidebar. */
function SidebarPinButton({
  collection,
  libraryId,
}: {
  collection: LibraryTabCollection;
  libraryId: number;
}) {
  const { togglePin, isPinned, canToggle } = useToggleSidebarPin();
  if (!canToggle) return null;
  const pinned = isPinned(libraryId, "collection", collection.id);
  return (
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
  );
}
