import { Star } from "lucide-react";
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
import type { HomeRow, Surface } from "@/lib/homeRows/types";
import { GROUP_ICONS } from "./rowIcons";

export interface RowLineProps extends Pick<
  ListRowProps,
  "handleProps" | "selection" | "menu" | "onOpen" | "ref" | "style" | "dragging" | "highlighted"
> {
  row: HomeRow;
  surface: Surface;
  pageLabel: string;
  description: DescriptionPart[];
  /** Where the row's poster peek comes from; null keeps its icon. */
  peek: PeekRequest | null;
  switchDisabled?: boolean;
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
  pageLabel,
  description,
  peek,
  switchDisabled,
  onShownChange,
  ...rest
}: RowLineProps) {
  return (
    <ListRow
      {...rest}
      id={row.id}
      title={row.title}
      collapsed={!row.shown}
      collapsedText={collapsedRowText(surface, pageLabel)}
      art={<PosterPeek icon={GROUP_ICONS[rowKindGroup(row.sectionType)]} request={peek} />}
      tags={
        <>
          {row.hero ? (
            <span className="bg-warning/15 text-warning ring-warning/30 inline-flex h-[22px] shrink-0 items-center gap-1 rounded-full px-2 text-[11.5px] font-semibold ring-1 ring-inset max-sm:hidden">
              <Star className="size-3" aria-hidden />
              Hero banner
            </span>
          ) : null}
          {row.own ? (
            <span className="bg-info/15 text-info ring-info/30 inline-flex h-[22px] shrink-0 items-center rounded-full px-2 text-[11.5px] font-semibold ring-1 ring-inset">
              Yours
            </span>
          ) : null}
        </>
      }
      meta={
        <>
          {row.hero ? (
            // Phones have no room for the tag next to the name.
            <span className="text-warning font-semibold sm:hidden">
              <Star className="mr-1 inline size-3 align-[-1px]" aria-hidden />
              Hero banner
              <MetaDot />
            </span>
          ) : null}
          <Description parts={description} />
          <MetaDot />
          {titleCount(row.itemLimit)}
        </>
      }
      shown={{
        checked: row.shown,
        disabled: switchDisabled,
        label: rowSwitchLabel(surface, row, pageLabel),
        onChange: onShownChange,
      }}
    />
  );
}
