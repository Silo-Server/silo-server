import type { ReactNode } from "react";
import {
  BookHeadphones,
  CalendarDays,
  Film,
  FolderOpen,
  Home,
  LayoutGrid,
  Library,
  Sparkles,
  Tv,
} from "lucide-react";
import type { UserLibrary } from "@/api/types";
import { menuItemKey } from "@/lib/uiCustomization";
import type { PrimaryMenuDocument } from "@/lib/uiCustomization";
import {
  buildLibraryCollectionCatalogHref,
  buildSectionCatalogHref,
  buildUserCollectionCatalogHref,
} from "@/pages/catalogSearchParams";

/**
 * Turns the profile's stored primary menu into the links a navigation surface
 * renders. Shared by AppSidebar and the WeFlix top bar so both surfaces offer
 * the same destinations in the same order — a menu item added in Settings must
 * appear in whichever shell the theme is using.
 */

export interface ResolvedPrimaryMenuLink {
  key: string;
  href: string;
  label: string;
  icon: ReactNode;
}

export function getLibraryIcon(type: string) {
  switch (type) {
    case "movies":
      return <Film className="h-[18px] w-[18px]" />;
    case "series":
      return <Tv className="h-[18px] w-[18px]" />;
    case "audiobook":
    case "audiobooks":
      return <BookHeadphones className="h-[18px] w-[18px]" />;
    default:
      return <Library className="h-[18px] w-[18px]" />;
  }
}

/**
 * `null` means the menu document has not loaded yet, which callers treat as
 * "show the built-in destinations" rather than "show nothing".
 */
export function resolvePrimaryMenuLinks(
  primaryMenu: PrimaryMenuDocument | null | undefined,
  libraries: UserLibrary[] | undefined,
): ResolvedPrimaryMenuLink[] | null {
  if (!primaryMenu) return null;

  return primaryMenu.items.flatMap((item): ResolvedPrimaryMenuLink[] => {
    const key = menuItemKey(item);
    if (item.type === "library") {
      const library = libraries?.find((candidate) => candidate.id === item.library_id);
      if (!library) return [];
      return [
        {
          key,
          href: `/library/${library.id}`,
          label: item.label || library.name,
          icon: getLibraryIcon(library.type),
        },
      ];
    }
    if (item.type === "section") {
      if (!libraries?.some((candidate) => candidate.id === item.library_id)) return [];
      return [
        {
          key,
          href: buildSectionCatalogHref({
            scope: "library",
            libraryId: item.library_id,
            sectionId: item.section_id,
            title: item.label,
          }),
          label: item.label,
          icon: <LayoutGrid className="h-[18px] w-[18px] shrink-0" />,
        },
      ];
    }
    if (item.type === "collection") {
      if (
        item.library_id !== undefined &&
        !libraries?.some((candidate) => candidate.id === item.library_id)
      ) {
        return [];
      }
      return [
        {
          key,
          href:
            item.library_id === undefined
              ? buildUserCollectionCatalogHref(item.collection_id, item.label)
              : buildLibraryCollectionCatalogHref(
                  item.collection_id,
                  item.label,
                  item.library_id,
                ),
          label: item.label,
          icon: <FolderOpen className="h-[18px] w-[18px] shrink-0" />,
        },
      ];
    }

    switch (item.destination) {
      case "home":
        return [
          {
            key,
            href: "/",
            label: "Home",
            icon: <Home className="h-[18px] w-[18px] shrink-0" />,
          },
        ];
      case "for_you":
        return [
          {
            key,
            href: "/recommendations",
            label: "For You",
            icon: <Sparkles className="h-[18px] w-[18px] shrink-0" />,
          },
        ];
      case "calendar":
        return [
          {
            key,
            href: "/calendar",
            label: "Calendar",
            icon: <CalendarDays className="h-[18px] w-[18px] shrink-0" />,
          },
        ];
      // These contract built-ins identify global media destinations, not a
      // particular library. Web does not currently have complete global
      // routes for all four media families (notably music), so omit them
      // instead of silently changing their meaning to the first matching
      // library. An explicit `library` menu item remains fully supported.
      case "movies":
      case "series":
      case "music":
      case "audiobooks":
        return [];
    }
  });
}

/**
 * The destinations a navigation surface shows before the profile's menu
 * document has loaded. Matches the built-ins the default menu document holds,
 * so the first paint does not offer a different set than the settled one.
 */
export function fallbackPrimaryMenuLinks(): ResolvedPrimaryMenuLink[] {
  return [
    {
      key: "builtin:for_you",
      href: "/recommendations",
      label: "For You",
      icon: <Sparkles className="h-[18px] w-[18px] shrink-0" />,
    },
    {
      key: "builtin:calendar",
      href: "/calendar",
      label: "Calendar",
      icon: <CalendarDays className="h-[18px] w-[18px] shrink-0" />,
    },
  ];
}
