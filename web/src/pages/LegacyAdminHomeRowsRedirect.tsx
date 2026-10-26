import { Navigate, useLocation } from "react-router";

/**
 * `/admin/home-rows` → `/admin/sections`. The page was briefly called Home
 * rows; links from then keep working, including any `?page=` selection.
 */
export default function LegacyAdminHomeRowsRedirect() {
  const { search, hash } = useLocation();
  return <Navigate to={`/admin/sections${search}${hash}`} replace />;
}
