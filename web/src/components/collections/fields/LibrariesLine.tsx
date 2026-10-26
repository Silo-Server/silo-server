import type { ReactNode } from "react";

import LibraryMultiSelect from "@/components/LibraryMultiSelect";

import { LIBRARIES_LINE_ID } from "./librariesLineFocus";

/**
 * The first line of a Contents panel: which libraries the collection draws on,
 * as "Titles from [Movies, Kids ▾]". `warning` sits under it, for example when
 * an untick would drop titles.
 */
export function LibrariesLine({
  lead,
  libraries,
  value,
  onChange,
  eligibleKinds,
  ineligibleReason,
  disabled,
  warning,
  allLabel,
}: {
  lead: string;
  libraries: Array<{ id: number; name: string; type?: string }>;
  value: number[];
  onChange: (libraryIds: number[]) => void;
  eligibleKinds?: string[];
  ineligibleReason?: string;
  disabled?: boolean;
  warning?: ReactNode;
  /** When none ticked means every library: how that reads, as in "All my libraries". */
  allLabel?: string;
}) {
  return (
    <div className="grid gap-2.5">
      <div id={LIBRARIES_LINE_ID} className="flex flex-wrap items-center gap-2.5">
        <span className="text-muted-foreground text-[14px]">{lead}</span>
        <LibraryMultiSelect
          libraries={libraries}
          value={value}
          onChange={onChange}
          eligibleKinds={eligibleKinds}
          ineligibleReason={ineligibleReason}
          hideAllOption={!allLabel}
          emptyLabel={allLabel ?? "Choose libraries"}
          triggerLabel={lead}
          triggerClassName="h-9 max-w-full justify-between gap-1 rounded-[10px] px-3 font-semibold"
          disabled={disabled}
        />
      </div>
      {warning}
    </div>
  );
}
