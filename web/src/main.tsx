import { createRoot } from "react-dom/client";
import App from "./App";
import { installPreloadErrorReload } from "./lib/reloadOnPreloadError";
import "./app.css";

installPreloadErrorReload();

const root = document.getElementById("root");
if (root === null) throw new Error("Root element #root not found");
createRoot(root).render(<App />);
