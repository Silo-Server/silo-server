import { useId, type ReactNode } from "react";
import { useImageLoaded } from "@/hooks/useImageLoaded";
import { Pin, PinOff, User } from "lucide-react";
import type { LibraryTabCollection } from "@/api/types";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useToggleSidebarPin } from "@/hooks/queries/sidebarPins";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { buildCollectionCatalogHref } from "@/pages/catalogSearchParams";

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
  pinnable = true,
}: {
  collection: LibraryTabCollection;
  kind: "regular" | "user_collections";
  /** The library a server collection opens in; unused for user collections. */
  libraryId?: number;
  /** Set on another profile's shared collection: shown as "by Name" under the title. */
  ownerName?: string;
  /** The line under the name. When set, the poster drops its count. */
  meta?: ReactNode;
  /** One tag under the meta line (Shared, say). */
  tag?: ReactNode;
  /** A ⋯ menu over the poster's top-right corner, always visible. */
  menu?: ReactNode;
  /** A drag handle over the poster's top-left corner. */
  handle?: ReactNode;
  /** A server collection's card offers to pin it to the sidebar. */
  pinnable?: boolean;
}) {
  const t = useT();
  const { loaded, onLoad, onError } = useImageLoaded(collection.poster_url);

  const isUserCollection = kind === "user_collections";
  const showPin = pinnable && !isUserCollection && libraryId !== undefined;
  const href = buildCollectionCatalogHref(collection, isUserCollection, libraryId);
  // A card with its own meta line draws it below the link instead.
  let subline: ReactNode = null;
  if (ownerName) {
    subline = (
      <div className="text-muted-foreground flex overflow-hidden text-xs whitespace-pre">
        {t("web.collections.posterCard.byOwner", {
          owner: (
            <span dir="auto" className="truncate">
              {ownerName}
            </span>
          ),
        })}
      </div>
    );
  } else if (meta === undefined && isUserCollection) {
    subline = (
      <div className="text-muted-foreground text-xs">
        {t("web.collections.posterCard.userCollection")}
      </div>
    );
  }

  return (
    <div className={`group/card relative w-full text-start ${handle ? "@container/poster" : ""}`}>
      <ViewTransitionLink to={href} className="block w-full text-start">
        <div className="media-card-image relative aspect-[2/3] overflow-hidden rounded-xl">
          {collection.poster_url ? (
            <img
              src={collection.poster_url}
              // The caption names the card.
              alt=""
              className={`h-full w-full object-cover transition duration-300 group-hover/card:scale-[1.05] ${
                loaded ? "opacity-100" : "opacity-0"
              }`}
              loading="lazy"
              onLoad={onLoad}
              onError={onError}
            />
          ) : (
            <div
              aria-hidden
              className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-sm"
            >
              {isUserCollection && <User className="h-5 w-5 opacity-60" />}
              {/* The spacers keep the title below the pin (top-2 plus a 36 px chip)
                  and above the count. With them the title never shrinks, so on a
                  card too short for all of it the spacers give way first. */}
              {showPin ? <div className="h-7 shrink" /> : null}
              <span
                dir="auto"
                className={`bidi-text line-clamp-3 font-medium ${showPin ? "shrink-0" : ""}`}
              >
                {collection.title}
              </span>
              {showPin && meta === undefined ? <div className="h-3 shrink" /> : null}
            </div>
          )}
          {meta === undefined ? (
            <span className="bg-background/60 text-foreground absolute end-2 bottom-2 rounded-md px-2 py-0.5 text-[0.6875rem] font-bold backdrop-blur-sm">
              {collection.item_count.toLocaleString(formatLocale())}
            </span>
          ) : null}
        </div>
        <div className="px-0.5 pt-2.5">
          <div dir="auto" className="bidi-text truncate text-[0.8125rem] font-semibold">
            {collection.title}
          </div>
          {subline}
        </div>
      </ViewTransitionLink>
      {meta !== undefined ? <div className="px-0.5">{meta}</div> : null}
      {tag ? <div className="px-0.5 pt-1.5">{tag}</div> : null}
      {/* With a handle, the two corner chips share the top edge; on the
          narrowest cards (about 88 px on a 320 px phone, or wider ones under
          a larger text size, since the chips scale with it) a tighter inset
          keeps a gap between them. Only a card with a handle is the poster
          container, so the menu keeps its inset everywhere else. */}
      {menu ? (
        <div className="absolute end-2 top-2 z-10 @max-[6rem]/poster:end-1.5 @max-[6rem]/poster:top-1.5">
          {menu}
        </div>
      ) : null}
      {handle ? (
        <div className="absolute start-2 top-2 z-10 @max-[6rem]/poster:start-1.5 @max-[6rem]/poster:top-1.5">
          {handle}
        </div>
      ) : null}
      {showPin ? <SidebarPinButton collection={collection} libraryId={libraryId} /> : null}
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
  const t = useT();
  const { togglePin, isPinned, canToggle } = useToggleSidebarPin();
  const titleId = useId();
  if (!canToggle) return null;
  const pinned = isPinned(libraryId, "collection", collection.id);
  const pinLabel = pinned
    ? t("web.collections.posterCard.unpinFromSidebar")
    : t("web.collections.posterCard.pinToSidebar");
  return (
    <>
      <span id={titleId} hidden>
        {collection.title}
      </span>
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
        // The dark poster chip of the collection actions menu, at the same size.
        className={`absolute start-2 top-2 flex size-8 items-center justify-center rounded-[10px] backdrop-blur-sm transition-all max-lg:size-9 ${
          pinned
            ? "bg-primary/90 text-primary-foreground"
            : "pointer-reveal bg-black/55 text-white group-hover/card:opacity-100 hover:bg-black/70"
        }`}
        title={pinLabel}
        aria-label={pinLabel}
        aria-describedby={titleId}
      >
        {pinned ? <PinOff className="size-4" /> : <Pin className="size-4" />}
      </button>
    </>
  );
}
