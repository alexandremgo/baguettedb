import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/schibsted-grotesk";
import "./styles/global.css";
import { loadConfig } from "./config";
import { App } from "./App";

const root = createRoot(document.getElementById("root")!);

loadConfig()
  .then(() => {
    root.render(
      <StrictMode>
        <App />
      </StrictMode>,
    );
  })
  .catch(() => {
    root.render(
      <div style={{ padding: "2rem", fontFamily: "system-ui" }}>
        <h1>Backend unreachable</h1>
        <p>
          The app loads its configuration from the backend. Start it (<code>make dev-up</code> for local
          development) and reload this page.
        </p>
      </div>,
    );
  });
