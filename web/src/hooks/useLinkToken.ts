import { useLocation } from "react-router";
import { linkToken } from "@/lib/linkToken";

/**
 * The token of the password reset or invitation link the current history
 * entry was opened with; "" when it was opened without one. The token lives
 * in the entry's state, not the URL, so this follows the router's location to
 * read it again when Back or Forward moves to another entry.
 */
export function useLinkToken(): string {
  useLocation();
  return linkToken();
}
