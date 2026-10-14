import type { ReactNode } from "react";

import { SHOW_TO_OTHER_PROFILES_HELP, SHOW_TO_OTHER_PROFILES_LABEL } from "@/lib/collections/copy";

import { ToggleRow } from "./fields/ToggleRow";

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
  return (
    <ToggleRow
      label={SHOW_TO_OTHER_PROFILES_LABEL}
      help={SHOW_TO_OTHER_PROFILES_HELP}
      checked={checked}
      onCheckedChange={onCheckedChange}
      disabled={disabled}
    >
      {children}
    </ToggleRow>
  );
}
