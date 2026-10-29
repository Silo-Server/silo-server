import { createRoot } from "react-dom/client";
import App from "./App";
import { installPreloadErrorReload } from "./lib/reloadOnPreloadError";
import { initPointerCapability } from "./lib/pointerCapability";
import "./fonts.css";
import "./app.css";
// After app.css: the WeFlix theme adds tokens and shell rules of its own, and
// the offset override at the end of it has to win against app.css.
import "./themes/weflix.css";

installPreloadErrorReload();
// Before render, so the first paint already knows whether hover reveals apply.
initPointerCapability();

const root = document.getElementById("root");
if (root === null) throw new Error("Root element #root not found");
createRoot(root).render(<App />);
