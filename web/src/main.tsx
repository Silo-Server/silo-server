import { createRoot } from "react-dom/client";
import App from "./App";
import "./app.css";

const root = document.getElementById("root");
if (root === null) throw new Error("Root element #root not found");
createRoot(root).render(<App />);

if ("serviceWorker" in navigator && "PushManager" in window) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => {
      // registration failure is non-fatal; push just won't be available
    });
  });
}
