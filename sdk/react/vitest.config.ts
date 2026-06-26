import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "jsdom",
    server: {
      deps: {
        inline: ["@trident-indexer/sdk"],
      },
    },
  },
  resolve: {
    preserveSymlinks: true,
  },
});
