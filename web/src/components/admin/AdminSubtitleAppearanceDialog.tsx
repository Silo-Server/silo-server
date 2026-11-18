import { useT } from "@/i18n";
import { useAutosavedDraft } from "@/hooks/useAutosavedDraft";
import { parseSubtitleAppearance, type SubtitleAppearance } from "@/lib/subtitleAppearance";
import { SubtitleAppearancePanelView } from "@/components/settings/SubtitleAppearancePanelView";
import type { AdminDeviceSetting } from "@/hooks/queries/admin/users";

interface AdminSubtitleAppearanceDialogProps {
  /**
   * The setting being edited. The parent mounts the dialog only while it is
   * open, so each open starts its draft from this value. The setting may be a
   * real override or a synthesized default-row from the manifest;
   * `isOverride` decides whether the reset action is offered.
   */
  setting: AdminDeviceSetting;
  /**
   * Whether the underlying setting is a real device override (vs. a
   * synthesized default that hasn't been written yet). Drives the footer
   * "Reset override" affordance.
   */
  isOverride: boolean;
  /** Close affordance — clear the parent's `setting` state. */
  onClose: () => void;
  /**
   * Persist a new value as a JSON string, settling once the server answers.
   * The parent fires the actual mutation (so the dialog stays decoupled from
   * the network layer) and reports failures.
   */
  onSave: (setting: AdminDeviceSetting, value: string) => Promise<unknown>;
  /** Remove the override on the device. Only invoked when `isOverride`. */
  onReset: (setting: AdminDeviceSetting) => void;
}

/**
 * Admin override editor for the `subtitle_appearance` device setting.
 * Reuses the same {@link SubtitleAppearancePanelView} the player presents
 * to end users — same preview, same controls — so admin overrides land in
 * exactly the same shape clients consume.
 *
 * Each change shows at once and saves in the background; a change the server
 * rejects drops back out of the preview, so the footer's "saved" status holds.
 */
export function AdminSubtitleAppearanceDialog({
  setting,
  isOverride,
  onClose,
  onSave,
  onReset,
}: AdminSubtitleAppearanceDialogProps) {
  const t = useT();
  const draft = useAutosavedDraft(parseSubtitleAppearance(setting.value));

  const handleChange = (patch: Partial<SubtitleAppearance>) =>
    draft.edit(patch, (next) => onSave(setting, JSON.stringify(next)));

  const handleReset = () => {
    draft.discardQueued();
    onReset(setting);
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
      value={draft.value}
      onChange={handleChange}
      onClose={onClose}
      canReset={isOverride}
      onReset={handleReset}
      resetLabel={t("web.admin.common.subtitleAppearanceDialog.resetOverride")}
      eyebrow={eyebrow}
      status={
        draft.saving
          ? t("web.admin.common.subtitleAppearanceDialog.saving")
          : isOverride
            ? t("web.admin.common.subtitleAppearanceDialog.savedAutomatically")
            : t("web.admin.common.subtitleAppearanceDialog.createsOverride")
      }
    />
  );
}
