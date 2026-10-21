import { defineConfig } from 'astro/config';
import tailwindcss from '@tailwindcss/vite';
import react from '@astrojs/react';
import node from '@astrojs/node';

// Issue #785: Require TRIDENT_MAINNET_SOROBAN_RPC_URL to be explicitly configured.
// Failing the build/start if missing ensures production never silently defaults to
// an unvetted third-party endpoint with no SLA.
const mainnetRpc = process.env.TRIDENT_MAINNET_SOROBAN_RPC_URL;
if (!mainnetRpc || !mainnetRpc.trim()) {
  throw new Error(
    '[explorer] TRIDENT_MAINNET_SOROBAN_RPC_URL is required but not configured. ' +
    'Provide an authenticated or SLA-backed Soroban RPC endpoint (e.g. https://soroban-mainnet.stellar.org). ' +
    'Defaults are disallowed to avoid third-party SLA risk (issue #785).'
  );
}

export default defineConfig({
  output: 'server',
  adapter: node({ mode: 'standalone' }),
  integrations: [react()],
  server: { port: 4321 },
  vite: {
    plugins: [tailwindcss()],
  },
});
