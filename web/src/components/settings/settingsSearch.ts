export interface SettingsSearchEntry {
  label: string;
  description?: string;
  keywords?: readonly string[];
}

export interface SettingsSearchItem {
  label: string;
  description?: string;
  keywords?: readonly string[];
  settings?: readonly SettingsSearchEntry[];
}

export interface SettingsSearchGroup<T extends SettingsSearchItem> {
  label: string;
  items: readonly T[];
}

function normalizeSearchText(value: string) {
  return value
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, " ")
    .trim();
}

function searchTokens(query: string) {
  const normalized = normalizeSearchText(query);
  return normalized ? normalized.split(/\s+/) : [];
}

function itemSearchText<T extends SettingsSearchItem>(group: SettingsSearchGroup<T>, item: T) {
  const settingText = item.settings?.flatMap((setting) => [
    setting.label,
    setting.description,
    ...(setting.keywords ?? []),
  ]);

  return normalizeSearchText(
    [group.label, item.label, item.description, ...(item.keywords ?? []), ...(settingText ?? [])]
      .filter(Boolean)
      .join(" "),
  );
}

function entrySearchText(entry: SettingsSearchEntry) {
  return normalizeSearchText([entry.label, entry.description, ...(entry.keywords ?? [])].join(" "));
}

function textMatchesTokens(text: string, tokens: string[]) {
  const words = text.split(/\s+/).filter(Boolean);

  return tokens.every((token) =>
    words.some((word) => word.startsWith(token) || (token.length >= 4 && word.includes(token))),
  );
}

export function filterSettingsSearchEntries(
  entries: readonly SettingsSearchEntry[] | undefined,
  query: string,
) {
  const tokens = searchTokens(query);

  if (!entries?.length || !tokens.length) {
    return [];
  }

  return entries.filter((entry) => textMatchesTokens(entrySearchText(entry), tokens));
}

export function filterSettingsSearchGroups<T extends SettingsSearchItem>(
  groups: readonly SettingsSearchGroup<T>[],
  query: string,
): SettingsSearchGroup<T>[] {
  const tokens = searchTokens(query);

  if (!tokens.length) {
    return groups.map((group) => ({ ...group, items: [...group.items] }));
  }

  return groups
    .map((group) => {
      const groupText = normalizeSearchText(group.label);
      const groupMatches = textMatchesTokens(groupText, tokens);
      const items = groupMatches
        ? [...group.items]
        : group.items.filter((item) => textMatchesTokens(itemSearchText(group, item), tokens));

      return { ...group, items };
    })
    .filter((group) => group.items.length > 0);
}

export function countSettingsSearchItems<T extends SettingsSearchItem>(
  groups: readonly SettingsSearchGroup<T>[],
) {
  return groups.reduce((count, group) => count + group.items.length, 0);
}
