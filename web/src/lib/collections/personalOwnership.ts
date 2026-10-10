// Personal collection ownership (#1615): a collection belongs to the profile
// that created it. Other profiles on the login see it read-only when it is
// shared. Lists, pickers and editors decide what to offer from these helpers.

interface OwnedCollection {
  creator_profile_id: string;
}

export interface ProfileName {
  id: string;
  name: string;
}

export interface SharedCollectionGroup<T> {
  owner: ProfileName;
  collections: T[];
}

/** True when profileId created the collection, so it may change it. */
export function isOwnCollection(collection: OwnedCollection, profileId?: string | null): boolean {
  return Boolean(profileId) && collection.creator_profile_id === profileId;
}

/** The owner's display name, or a neutral label for a profile no longer listed. */
export function ownerName(profiles: readonly ProfileName[], ownerId: string): string {
  return profiles.find((profile) => profile.id === ownerId)?.name ?? "Another profile";
}

/**
 * Splits the profile's listed collections into its own (in listed order) and
 * other profiles' shared collections, grouped by owner in the login's
 * profile-list order. Owners missing from the list come last.
 */
export function partitionPersonalCollections<T extends OwnedCollection>(
  collections: readonly T[],
  profileId: string | null | undefined,
  profiles: readonly ProfileName[],
): { own: T[]; shared: SharedCollectionGroup<T>[] } {
  const own: T[] = [];
  const byOwner = new Map<string, T[]>();
  for (const collection of collections) {
    if (isOwnCollection(collection, profileId)) {
      own.push(collection);
      continue;
    }
    const owned = byOwner.get(collection.creator_profile_id) ?? [];
    owned.push(collection);
    byOwner.set(collection.creator_profile_id, owned);
  }
  const position = (ownerId: string) => {
    const index = profiles.findIndex((profile) => profile.id === ownerId);
    return index < 0 ? profiles.length : index;
  };
  const shared = [...byOwner.entries()]
    .sort(([left], [right]) => position(left) - position(right))
    .map(([ownerId, owned]) => ({
      owner: { id: ownerId, name: ownerName(profiles, ownerId) },
      collections: owned,
    }));
  return { own, shared };
}
