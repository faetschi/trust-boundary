// The theme bootstrap is imported first, so the data-theme attribute is set before the
// first React render. The stylesheets follow, tokens before base.
import "./theme/bootstrap";
import "./styles/tokens.css";
import "./styles/base.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";

const container = document.getElementById("root");
if (!container) {
  throw new Error("tbound web chat: #root is missing from index.html");
}

createRoot(container).render(
  <StrictMode>
    <App version={__APP_VERSION__} />
  </StrictMode>,
);
