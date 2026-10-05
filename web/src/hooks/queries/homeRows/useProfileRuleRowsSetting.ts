import { useCallback, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  fetchAdminSectionSettings,
  updateAdminSectionSettings,
  type AdminSectionSettingsSnapshot,
} from "@/api/adminSectionSettings";
import { V2ProblemError } from "@/api/v2/request";

const settingKey = ["admin-section-settings"] as const;
const settingQuery = {
  queryKey: settingKey,
  queryFn: ({ signal }: { signal: AbortSignal }) => fetchAdminSectionSettings(signal),
  staleTime: 0,
};
/** What Settings > Home Screen reads the same setting through. */
const profileFlagsKey = ["profile-section-flags"] as const;

export interface ProfileRuleRowsSetting {
  /** The value to show: the one being saved while a change is in flight, else the server's. */
  allowed: boolean | null;
  /** Not read yet, or a change is saving. */
  busy: boolean;
  /** The read failed and nothing was read before. */
  failed: boolean;
  /** The server refuses changes from this account (demo mode): its reason. */
  refusal: string | null;
  /** Changes the setting over the version last read. Never rejects; it reports by toast. */
  set(allowed: boolean): Promise<void>;
}

/**
 * "Let profiles add rule rows" on admin Home rows. The setting is read while
 * `active` (the More menu is open), so each open shows the current value, and
 * a change is written over the version that was read: a change made elsewhere
 * in between fails with 412, and the value is read again rather than overwritten.
 */
export function useProfileRuleRowsSetting(active: boolean): ProfileRuleRowsSetting {
  const queryClient = useQueryClient();
  const [refusal, setRefusal] = useState<string | null>(null);
  const query = useQuery({ ...settingQuery, enabled: active });
  const mutation = useMutation({
    mutationFn: ({ allowed, etag }: { allowed: boolean; etag: string }) =>
      updateAdminSectionSettings(allowed, etag),
    // A read that started before this write must not land after it.
    onMutate: () => queryClient.cancelQueries({ queryKey: settingKey }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: profileFlagsKey }),
  });

  const snapshot = query.data;
  const { mutateAsync } = mutation;
  const set = useCallback(
    async (allowed: boolean) => {
      if (!snapshot || refusal) return;
      try {
        const saved = await mutateAsync({ allowed, etag: snapshot.etag });
        if (saved.etag)
          queryClient.setQueryData<AdminSectionSettingsSnapshot>(settingKey, {
            allowProfileRuleRows: saved.allowProfileRuleRows,
            etag: saved.etag,
          });
        else await queryClient.invalidateQueries({ queryKey: settingKey });
        toast.success(
          saved.allowProfileRuleRows
            ? "Profiles can now add rule rows."
            : "Profiles can no longer add rule rows.",
        );
      } catch (error) {
        if (error instanceof V2ProblemError && error.status === 412) {
          try {
            const current = await queryClient.fetchQuery(settingQuery);
            toast.info(
              `Changed elsewhere; it's now ${current.allowProfileRuleRows ? "On" : "Off"}.`,
            );
          } catch (reread) {
            toast.error(reread instanceof Error ? reread.message : "Could not read this setting.");
          }
          return;
        }
        const message = error instanceof Error ? error.message : "Could not change this setting.";
        if (error instanceof V2ProblemError && error.status === 403) setRefusal(message);
        toast.error(message);
      }
    },
    [mutateAsync, queryClient, refusal, snapshot],
  );

  return {
    allowed: mutation.isPending
      ? mutation.variables.allowed
      : (snapshot?.allowProfileRuleRows ?? null),
    busy: !snapshot || mutation.isPending,
    failed: query.isError && !snapshot,
    refusal,
    set,
  };
}
