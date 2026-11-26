import { userDetailTabSearch } from "@/pages/admin-users/detail/userDetailTabs";

/**
 * The element ID of a device's row in a Silo app devices list, which a link
 * to that device targets (`#device-<id>`); the list itself is `#devices`.
 */
export function deviceAnchor(id: string) {
  return `device-${id}`;
}

/**
 * Where a person's own Silo app devices are listed and removed; `anchor`
 * names one device's row (deviceAnchor) instead of the list.
 */
export function ownDevicesPath(anchor = "devices") {
  return `/settings/sessions#${anchor}`;
}

/** Where an administrator lists and removes one account's Silo app devices. */
export function adminDevicesPath(userId: number | string, anchor = "devices") {
  return `/admin/users/${userId}${userDetailTabSearch("sign-in")}#${anchor}`;
}
