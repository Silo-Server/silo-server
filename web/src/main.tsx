import { createRoot } from "react-dom/client";
import App from "./App";
import { installPreloadErrorReload } from "./lib/reloadOnPreloadError";
import { initPointerCapability } from "./lib/pointerCapability";
import { installLinkTokenHandling } from "./lib/linkToken";
import { installSpatialNavigation } from "./lib/spatialNavigation";
import "./fonts.css";
import "./app.css";

// First, so nothing that reads the address sees a reset or invitation token.
installLinkTokenHandling();
installPreloadErrorReload();
// Before render, so the first paint already knows whether hover reveals apply.
initPointerCapability();
// Remotes and D-pads move focus with the arrow keys.
installSpatialNavigation();

const root = document.getElementById("root");
if (root === null) throw new Error("Root element #root not found");
createRoot(root).render(<App />);
