import { useState } from "react";
import { useT } from "@/i18n";
import { parseSubtitleAppearance, type SubtitleAppearance } from "@/lib/subtitleAppearance";
import { SubtitleAppearancePanelView } from "@/components/settings/SubtitleAppearancePanelView";
import type { AdminDeviceSetting } from "@/hooks/queries/admin/users";

interface AdminSubtitleAppearanceDialogProps {
  /**
   * The setting being edited. When null the dialog is hidden. The setting
   * may be a real override or a synthesized default-row from the manifest;
   * `isOverride` decides whether the reset action is offered.
   */
  setting: AdminDeviceSetting | null;
  /**
   * Whether the underlying setting is a real device override (vs. a
   * synthesized default that hasn't been written yet). Drives the footer
   * "Reset override" affordance.
   */
  isOverride: boolean;
  /** Close affordance — clear the parent's `setting` state. */
  onClose: () => void;
  /**
   * Persist a new value as a JSON string. The parent fires the actual
   * mutation (so the dialog stays decoupled from the network layer).
   */
  onSave: (setting: AdminDeviceSetting, value: string) => void;
  /** Remove the override on the device. Only invoked when `isOverride`. */
  onReset: (setting: AdminDeviceSetting) => void;
  /** Pending mutation flag — surfaces in the footer status. */
  saving?: boolean;
}

/**
 * Admin override editor for the `subtitle_appearance` device setting.
 * Reuses the same {@link SubtitleAppearancePanelView} the player presents
 * to end users — same preview, same controls — so admin overrides land in
 * exactly the same shape clients consume.
 *
 * The local appearance state is kept optimistic so dragging a slider feels
 * instantaneous; whenever the parent's `setting.value` flips (because the
 * admin device-detail cache invalidated after a save), we resync.
 */
export function AdminSubtitleAppearanceDialog({
  setting,
  isOverride,
  onClose,
  onSave,
  onReset,
  saving = false,
}: AdminSubtitleAppearanceDialogProps) {
  const t = useT();
  const settingKey = setting?.key;
  const settingValue = setting?.value ?? null;

  const [appearance, setAppearance] = useState<SubtitleAppearance>(() =>
    parseSubtitleAppearance(settingValue),
  );
  // Use the React "adjust state during render" pattern to resync optimistic
  // state when the underlying setting changes — either because the dialog
  // was opened on a different setting, or because a save round-tripped and
  // produced a fresh `setting.value` from the cache. Avoids the cascading-
  // render warning that a useEffect would trigger.
  const [prev, setPrev] = useState<{ key: string | undefined; value: string | null }>({
    key: settingKey,
    value: settingValue,
  });
  if (prev.key !== settingKey || prev.value !== settingValue) {
    setPrev({ key: settingKey, value: settingValue });
    setAppearance(parseSubtitleAppearance(settingValue));
  }

  if (!setting) return null;

  const handleChange = (patch: Partial<SubtitleAppearance>) => {
    const next = { ...appearance, ...patch };
    setAppearance(next);
    onSave(setting, JSON.stringify(next));
  };

  const handleReset = () => {
    onReset(setting);
    // The cache will repopulate `setting.value` to the empty/default string;
    // the effect above takes care of resyncing local state.
    onClose();
  };

  // Eyebrow gives admin context that the player panel lacks: which user +
  // device + profile the override is being scoped to.
  const profileLabel =
    setting.profile_name?.trim() ||
    setting.profile_id ||
    t("web.admin.common.subtitleAppearanceDialog.defaultProfile");
  const deviceLabel =
    setting.device_name?.trim() || t("web.admin.common.subtitleAppearanceDialog.unnamedDevice");
  const eyebrow = `${profileLabel} · ${deviceLabel}`;

  return (
    <SubtitleAppearancePanelView
      open
      value={appearance}
      onChange={handleChange}
      onClose={onClose}
      canReset={isOverride}
      onReset={handleReset}
      resetLabel={t("web.admin.common.subtitleAppearanceDialog.resetOverride")}
      eyebrow={eyebrow}
      status={
        saving
          ? t("web.admin.common.subtitleAppearanceDialog.saving")
          : isOverride
            ? t("web.admin.common.subtitleAppearanceDialog.savedAutomatically")
            : t("web.admin.common.subtitleAppearanceDialog.createsOverride")
      }
    />
  );
}
