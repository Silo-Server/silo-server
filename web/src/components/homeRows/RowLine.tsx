import { Star, type LucideIcon } from "lucide-react";
import { ListRow, MetaDot, type ListRowProps } from "@/components/calm/ListRow";
import { PosterPeek } from "@/components/calm/PosterPeek";
import type { PeekRequest } from "@/components/calm/usePeekLimiter";
import { rowKindGroup } from "@/lib/homeRows/catalog";
import {
  collapsedRowText,
  rowSwitchLabel,
  titleCount,
  type DescriptionPart,
} from "@/lib/homeRows/describe";
import type { HomeRow, PageRef, Surface } from "@/lib/homeRows/types";
import { useT } from "@/i18n";
import { useHomeRowTitle } from "@/lib/sectionTitle";
import { GROUP_ICONS } from "./rowIcons";

export interface RowLineProps extends Pick<
  ListRowProps,
  "handleProps" | "selection" | "menu" | "onOpen" | "ref" | "style" | "dragging" | "highlighted"
> {
  row: HomeRow;
  surface: Surface;
  page: PageRef;
  pageLabel: string;
  description: DescriptionPart[];
  /** The row's icon; its kind's group icon when absent. */
  icon?: LucideIcon;
  /** Where the row's poster peek comes from; null keeps its icon. */
  peek: PeekRequest | null;
  switchDisabled?: boolean;
  /**
   * The row is the page's hero banner (its first shown hero) and an admin kept
   * it off the apps. A later hero reaches the apps as a regular row, so only
   * the banner is tagged web only.
   */
  webOnlyBanner?: boolean;
  onShownChange: (shown: boolean) => void;
}

function Description({ parts }: { parts: DescriptionPart[] }) {
  return parts.map((part, index) => {
    if (typeof part === "string") return part;
    if ("warning" in part)
      return (
        <span key={index} className="text-warning font-medium">
          {part.warning}
        </span>
      );
    return (
      <b key={index} className="text-foreground/75 font-medium">
        {part.strong}
      </b>
    );
  });
}

/**
 * One Home row in the list. Off (admin) and hidden (profile) rows collapse to
 * a dashed line.
 */
export function RowLine({
  row,
  surface,
  page,
  pageLabel,
  description,
  icon,
  peek,
  switchDisabled,
  webOnlyBanner = false,
  onShownChange,
  ...rest
}: RowLineProps) {
  const t = useT();
  const rowTitle = useHomeRowTitle();
  return (
    <ListRow
      {...rest}
      id={row.id}
      title={rowTitle(row)}
      collapsed={!row.shown}
      collapsedText={collapsedRowText(surface, page, pageLabel)}
      art={<PosterPeek icon={icon ?? GROUP_ICONS[rowKindGroup(row.sectionType)]} request={peek} />}
      tags={
        <>
          {row.hero ? (
            <span className="bg-warning/15 text-warning ring-warning/30 inline-flex h-[22px] shrink-0 items-center gap-1 rounded-full px-2 text-xs font-semibold ring-1 ring-inset max-sm:hidden">
              <Star className="size-3" aria-hidden />
              {webOnlyBanner
                ? t("web.home.rows.line.heroBannerWebOnly")
                : t("web.home.rows.line.heroBanner")}
            </span>
          ) : null}
          {row.own ? (
            <span className="bg-info/15 text-info ring-info/30 inline-flex h-[22px] shrink-0 items-center rounded-full px-2 text-xs font-semibold ring-1 ring-inset">
              {t("web.home.rows.line.yours")}
            </span>
          ) : null}
        </>
      }
      meta={
        <>
          {row.hero ? (
            // Phones have no room for the tag next to the name.
            <span className="text-warning font-semibold sm:hidden">
              <Star className="me-1 inline size-3 align-[-1px]" aria-hidden />
              {/* The short label fits the phone line, which the description shares. */}
              {webOnlyBanner
                ? t("web.home.rows.line.webOnlyHero")
                : t("web.home.rows.line.heroBanner")}
              <MetaDot />
            </span>
          ) : null}
          <Description parts={description} />
          {row.itemLimit > 0 ? (
            <>
              <MetaDot />
              {titleCount(row.itemLimit)}
            </>
          ) : null}
        </>
      }
      shown={{
        checked: row.shown,
        disabled: switchDisabled,
        label: rowSwitchLabel(surface, row, page, pageLabel),
        onChange: onShownChange,
      }}
    />
  );
}
