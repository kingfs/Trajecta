import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../../internal/monitor/ui/dist",
    emptyOutDir: true,
    assetsDir: "assets",
    chunkSizeWarningLimit: 900,
  },
});
