import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// `npm run dev` proxies API calls to a control plane running on :8080.
const control = process.env.FSG_CONTROL_URL ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": control,
      "/healthz": control,
      "/readyz": control,
    },
  },
});
