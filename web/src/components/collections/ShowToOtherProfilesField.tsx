import type { ReactNode } from "react";

import { showToOtherProfilesHelp, showToOtherProfilesLabel } from "@/lib/collections/copy";

import { ToggleRow } from "./fields/ToggleRow";
import { useAppLanguage } from "@/i18n";

/**
 * The one sharing switch of a personal collection (#1615): on, every profile
 * on the login sees it read-only; off, only its creator does. `children` sits
 * under the switch (the unshare warning, say).
 */
export function ShowToOtherProfilesField({
  checked,
  onCheckedChange,
  disabled = false,
  children,
}: {
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
  children?: ReactNode;
}) {
  useAppLanguage();
  return (
    <ToggleRow
      label={showToOtherProfilesLabel()}
      help={showToOtherProfilesHelp()}
      checked={checked}
      onCheckedChange={onCheckedChange}
      disabled={disabled}
    >
      {children}
    </ToggleRow>
  );
}
