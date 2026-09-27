import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminDeviceSummary, AdminDeviceDetail } from "@/api/types";
import { requireAdminUserAuthority } from "./adminUsers";
import { v2, type V2Result } from "./request";

type Device = V2Result<"GET /api/v2/admin/devices">["items"][number];
function userID(raw: string) {
  const id = Number(raw);
  if (!Number.isSafeInteger(id) || id <= 0) throw new Error("Unsupported account identifier.");
  return id;
}
function deviceOf(row: Device): AdminDeviceSummary {
  return {
    ...row,
    user_id: userID(row.user_id),
    last_updated: row.last_updated ?? "",
    profiles: row.profiles.map((p) => ({ ...p, last_updated: p.last_updated ?? "" })),
  };
}
export async function listAdminDevices(
  context: ProfileRequestContextSnapshot,
): Promise<AdminDeviceSummary[]> {
  const items: AdminDeviceSummary[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  for (let pageIndex = 0; pageIndex < 100; pageIndex++) {
    requireAdminUserAuthority(context);
    const result = await v2("GET /api/v2/admin/devices", {
      profileContext: context,
      query: { limit: 100, cursor },
    });
    requireAdminUserAuthority(context);
    if (!result.page || typeof result.page.has_more !== "boolean")
      throw new Error("Invalid device page.");
    items.push(...result.items.map(deviceOf));
    if (!result.page.has_more) return items;
    const next = result.page.next_cursor;
    if (!next || seen.has(next)) throw new Error("Invalid device continuation.");
    seen.add(next);
    cursor = next;
  }
  throw new Error("Too many devices to display. Reload the list.");
}
export async function getAdminDevice(
  user: number,
  device: string,
  context: ProfileRequestContextSnapshot,
): Promise<AdminDeviceDetail> {
  requireAdminUserAuthority(context);
  const row = await v2("GET /api/v2/admin/devices/{user_id}/{device_id}", {
    profileContext: context,
    path: { user_id: String(user), device_id: device },
  });
  requireAdminUserAuthority(context);
  return {
    ...deviceOf(row),
    settings: row.settings.map((s) => ({
      ...s,
      user_id: userID(s.user_id),
      updated_at: s.updated_at ?? "",
    })),
  };
}
