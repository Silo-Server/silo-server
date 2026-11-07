import { createRoot } from "react-dom/client";
import App from "./App";
import { installPreloadErrorReload } from "./lib/reloadOnPreloadError";
import { initPointerCapability } from "./lib/pointerCapability";
import { installSpatialNavigation } from "./lib/spatialNavigation";
import "./fonts.css";
import "./app.css";

installPreloadErrorReload();
// Before render, so the first paint already knows whether hover reveals apply.
initPointerCapability();
// Remotes and D-pads move focus with the arrow keys.
installSpatialNavigation();

const root = document.getElementById("root");
if (root === null) throw new Error("Root element #root not found");
createRoot(root).render(<App />);
