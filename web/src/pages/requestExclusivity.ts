import type { PluginAdminFormField } from "@/api/types";

export type ExclusivityCard = {
  key: string;
  installationId: string;
  config: Record<string, unknown>;
};

function isTruthy(value: unknown): boolean {
  return value === true || value === "true";
}

// applyExclusivity clears a mutually-exclusive field on sibling cards when the
// changed card turns it on. A field with exclusive_group_field=G permits at most
// one card per distinct value of config[G] (within the same installation) to
// hold that field truthy. Generic — no plugin-specific keys.
export function applyExclusivity(
  cards: ExclusivityCard[],
  changedKey: string,
  nextConfig: Record<string, unknown>,
  fieldsFor: (installationId: string) => PluginAdminFormField[],
): ExclusivityCard[] {
  const changed = cards.find((card) => card.key === changedKey);
  const withChange = cards.map((card) =>
    card.key === changedKey ? { ...card, config: nextConfig } : card,
  );
  if (!changed) return withChange;

  const exclusive = fieldsFor(changed.installationId).filter(
    (field) => field.exclusive_group_field && isTruthy(nextConfig[field.key]),
  );
  if (exclusive.length === 0) return withChange;

  return withChange.map((card) => {
    if (card.key === changedKey || card.installationId !== changed.installationId) {
      return card;
    }
    let config = card.config;
    let mutated = false;
    for (const field of exclusive) {
      const group = field.exclusive_group_field as string;
      if (isTruthy(card.config[field.key]) && card.config[group] === nextConfig[group]) {
        if (!mutated) {
          config = { ...config };
          mutated = true;
        }
        config[field.key] = false;
      }
    }
    return mutated ? { ...card, config } : card;
  });
}
