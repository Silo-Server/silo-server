import { Navigate, useLocation } from "react-router";

/**
 * `/admin/sections` → `/admin/home-rows`. The page was renamed; bookmarks and
 * older links keep working, including any `?page=` selection.
 */
export default function LegacyAdminSectionsRedirect() {
  const { search, hash } = useLocation();
  return <Navigate to={`/admin/home-rows${search}${hash}`} replace />;
}
