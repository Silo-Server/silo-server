import { Eye, EyeOff, Trash2 } from "lucide-react";
import { SelectModeBar as CalmSelectModeBar } from "@/components/calm/SelectModeBar";
import { useT } from "@/i18n";

/** The most rows one select-mode action works on, matching the server's bulk limits. */
export const MAX_SELECTED_ROWS = 100;

/** Select mode's bar on a Home rows page: turn the picked rows on or off, or delete them. */
export function SelectModeBar({
  count,
  busy = false,
  onTurnOn,
  onTurnOff,
  onDelete,
}: {
  count: number;
  busy?: boolean;
  onTurnOn: () => void;
  onTurnOff: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  return (
    <CalmSelectModeBar
      count={count}
      limit={MAX_SELECTED_ROWS}
      noun="rows"
      busy={busy}
      actions={[
        { key: "on", label: t("web.home.rows.select.turnOn"), icon: Eye, onClick: onTurnOn },
        { key: "off", label: t("web.home.rows.select.turnOff"), icon: EyeOff, onClick: onTurnOff },
        {
          key: "delete",
          label: t("web.home.rows.select.delete"),
          icon: Trash2,
          onClick: onDelete,
          destructive: true,
          separated: true,
        },
      ]}
    />
  );
}
