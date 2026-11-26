import { Navigate, useLocation } from "react-router";

/**
 * Live activity is the first tab of Insights. Old links and bookmarks to
 * `/admin/activity` open that tab, and its old `?view=preparations` view opens
 * the Downloads page that took it over.
 */
export default function LegacyAdminActivityRedirect() {
  const { search } = useLocation();
  if (new URLSearchParams(search).get("view") === "preparations") {
    return <Navigate to="/admin/downloads?tab=preparation" replace />;
  }
  return <Navigate to="/admin/insights" replace />;
}
