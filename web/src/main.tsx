import { createRoot } from "react-dom/client";
import { App } from "./App";
import { followPanelTheme } from "./lib/theme";
import { store } from "./state/store";
import "./styles.css";

followPanelTheme();
createRoot(document.getElementById("root") as HTMLElement).render(<App />);
void store.start();
