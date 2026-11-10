import type { AICapability, AIProvider } from "@/hooks/queries/admin/aiProviders";
import { t } from "@/i18n";

/** Display names of the AI capabilities, in the current language when read. */
export const AI_CAPABILITY_LABELS: Readonly<Record<AICapability, string>> = {
  get chat() {
    return t("web.admin.settings.lib.aiProviders.capability.chat");
  },
  get embeddings() {
    return t("web.admin.settings.lib.aiProviders.capability.embeddings");
  },
  get speech() {
    return t("web.admin.settings.lib.aiProviders.capability.speech");
  },
};

export function hostLabel(rawURL: string): string {
  const trimmed = rawURL.trim();
  if (!trimmed) return "";
  try {
    return new URL(trimmed.includes("://") ? trimmed : `https://${trimmed}`).host;
  } catch {
    return trimmed;
  }
}

/**
 * Options for a job's provider select: every provider that serves the
 * capability, plus the stored choice even when it no longer qualifies, so the
 * select never silently shows a different provider than the one saved.
 */
export function providerOptions(
  providers: AIProvider[],
  capability: AICapability,
  selected: string,
): { value: string; label: string; disabled?: boolean }[] {
  const options: { value: string; label: string; disabled?: boolean }[] = providers
    .filter((provider) => provider.capabilities.includes(capability))
    .map((provider) => ({ value: provider.id, label: provider.name }));
  if (selected && !options.some((option) => option.value === selected)) {
    const stale = providers.find((provider) => provider.id === selected);
    options.push({
      value: selected,
      label: stale
        ? t("web.admin.settings.lib.aiProviders.option.cannotServe", { name: stale.name })
        : t("web.admin.settings.lib.aiProviders.option.removed"),
      disabled: true,
    });
  }
  return options;
}
