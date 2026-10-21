/**
 * Lighthouse CI configuration for Trident Explorer.
 *
 * Configures desktop emulation and Core Web Vitals targets as defined in
 * explorer/PERFORMANCE_BUDGET.md.
 */

export default {
  extends: 'lighthouse:default',
  settings: {
    formFactor: 'desktop',
    screenEmulation: {
      mobile: false,
      width: 1350,
      height: 940,
      deviceScaleFactor: 1,
      disabled: false,
    },
    throttling: {
      rttMs: 40,
      throughputKbps: 10240,
      cpuSlowdownMultiplier: 1,
      requestLatencyMs: 0,
      downloadThroughputKbps: 0,
      uploadThroughputKbps: 0,
    },
    skipAudits: [
      'uses-http2',
    ],
  },
};
