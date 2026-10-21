/**
 * Performance test suite for explorer against realistic testnet data volumes.
 * Measures API latency, enforces budget limits, and detects regressions against baseline.
 *
 * Usage:
 *   npm run perf-test -- --api-url https://api.testnet.trident.dev --api-key YOUR_KEY
 *   npm run perf-test -- --record-baseline
 *   npm run perf-test -- --baseline-file ./PERFORMANCE_BASELINE.json --regression-threshold 10
 */

import { performance } from 'perf_hooks';
import * as fs from 'fs';
import * as path from 'path';

interface PerfResult {
  route: string;
  metric: string;
  value: number;
  unit: string;
  budget: number;
  ok: boolean;
  baseline?: number;
  regressionPct?: number;
  regressionOk?: boolean;
}

interface BaselineFile {
  version: string;
  recordedAt: string;
  dataset: string;
  metrics: Record<string, number>;
}

const results: PerfResult[] = [];

// Parse CLI args
const args = process.argv.slice(2);
const getArg = (name: string): string | null => {
  const idx = args.indexOf(name);
  return idx !== -1 && idx + 1 < args.length ? args[idx + 1] : null;
};
const hasFlag = (name: string): boolean => args.includes(name);

const apiUrl = getArg('--api-url') || process.env.API_URL || 'https://api.testnet.trident.dev';
const apiKey = getArg('--api-key') || process.env.API_KEY || '';
const testContractId = getArg('--contract-id') || 'CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4';
const baselineFilePath = getArg('--baseline-file') || path.resolve(process.cwd(), 'PERFORMANCE_BASELINE.json');
const shouldRecordBaseline = hasFlag('--record-baseline');
const regressionThreshold = parseFloat(getArg('--regression-threshold') || '10');
const mockLatenciesArg = getArg('--mock-latencies');
const mockLatencies: Record<string, number> | null = mockLatenciesArg ? JSON.parse(mockLatenciesArg) : null;

// Load existing baseline if available
let baselineData: BaselineFile | null = null;
if (fs.existsSync(baselineFilePath)) {
  try {
    const raw = fs.readFileSync(baselineFilePath, 'utf8');
    baselineData = JSON.parse(raw);
  } catch (err: any) {
    console.warn(`Warning: failed to parse baseline file at ${baselineFilePath}: ${err.message}`);
  }
}

async function time(label: string, metricKey: string, fn: () => Promise<any>): Promise<number> {
  if (mockLatencies && typeof mockLatencies[metricKey] === 'number') {
    const duration = mockLatencies[metricKey];
    console.log(`  ${label} (mocked): ${duration.toFixed(2)}ms`);
    return duration;
  }
  const start = performance.now();
  await fn();
  const duration = performance.now() - start;
  console.log(`  ${label}: ${duration.toFixed(2)}ms`);
  return duration;
}

function checkBudget(route: string, metric: string, value: number, budget: number, unit: string = 'ms'): void {
  const metricKey = `${route}:${metric}`;
  const budgetOk = value <= budget;
  let regressionOk = true;
  let regressionPct: number | undefined = undefined;
  let baselineVal: number | undefined = undefined;

  if (baselineData && baselineData.metrics && typeof baselineData.metrics[metricKey] === 'number') {
    baselineVal = baselineData.metrics[metricKey];
    regressionPct = ((value - baselineVal) / baselineVal) * 100;
    if (regressionPct > regressionThreshold) {
      regressionOk = false;
    }
  }

  const ok = budgetOk && regressionOk;
  results.push({
    route,
    metric,
    value,
    budget,
    unit,
    ok,
    baseline: baselineVal,
    regressionPct,
    regressionOk,
  });

  const symbol = ok ? '✓' : '✗';
  const color = ok ? '\x1b[32m' : '\x1b[31m';
  let detail = `${value.toFixed(2)}${unit} (budget: ${budget}${unit})`;
  if (baselineVal !== undefined && regressionPct !== undefined) {
    const regSign = regressionPct >= 0 ? '+' : '';
    detail += ` [baseline: ${baselineVal.toFixed(2)}${unit}, delta: ${regSign}${regressionPct.toFixed(1)}%]`;
  }
  console.log(`${color}  ${symbol} ${metric}: ${detail}\x1b[0m`);
}

async function runTests() {
  console.log('\n📊 Explorer Performance Tests\n');
  console.log(`API: ${apiUrl}`);
  console.log(`Contract: ${testContractId}`);
  if (baselineData) {
    console.log(`Baseline: ${baselineFilePath} (threshold: +${regressionThreshold}% max regression)`);
  }
  if (shouldRecordBaseline) {
    console.log(`Mode: Recording new baseline to ${baselineFilePath}`);
  }
  console.log('');

  // Test 1: Home ticker API (10 recent events)
  console.log('Test 1: Home ticker (recent events)');
  const homeLatency = await time('  /v1/events (limit=10)', 'home:API latency', async () => {
    const url = new URL(`${apiUrl}/v1/events`);
    url.searchParams.set('limit', '10');
    const res = await fetch(url.toString(), {
      headers: apiKey ? { 'X-API-Key': apiKey } : {},
    });
    if (!res.ok) throw new Error(`API ${res.status}`);
    await res.json();
  });
  checkBudget('home', 'API latency', homeLatency, 300);

  // Test 2: Contract listing API (25 events with filter)
  console.log('\nTest 2: Contract listing (25 paginated events)');
  const contractLatency = await time('  /v1/events (contractId filter, limit=25)', 'contract:API latency', async () => {
    const url = new URL(`${apiUrl}/v1/events`);
    url.searchParams.set('contractId', testContractId);
    url.searchParams.set('limit', '25');
    const res = await fetch(url.toString(), {
      headers: apiKey ? { 'X-API-Key': apiKey } : {},
    });
    if (!res.ok) throw new Error(`API ${res.status}`);
    await res.json();
  });
  checkBudget('contract', 'API latency', contractLatency, 500);

  // Test 3: Single event API
  console.log('\nTest 3: Event detail (single event)');
  let eventId: string | null = null;
  if (!mockLatencies) {
    try {
      await time('  /v1/events (fetch first event for detail test)', 'event-detail:lookup', async () => {
        const url = new URL(`${apiUrl}/v1/events`);
        url.searchParams.set('limit', '1');
        const res = await fetch(url.toString(), {
          headers: apiKey ? { 'X-API-Key': apiKey } : {},
        });
        if (!res.ok) throw new Error(`API ${res.status}`);
        const data = await res.json();
        if (data.events?.length > 0) eventId = data.events[0].id;
      });
    } catch (_) {
      // Best-effort lookup
    }
  }

  const detailEventId: string = eventId || 'mock-event-id';
  const eventLatency = await time('  /v1/events/:id', 'event-detail:API latency', async () => {
    const res = await fetch(`${apiUrl}/v1/events/${encodeURIComponent(detailEventId)}`, {
      headers: apiKey ? { 'X-API-Key': apiKey } : {},
    });
    if (!res.ok) throw new Error(`API ${res.status}`);
    await res.json();
  });
  checkBudget('event-detail', 'API latency', eventLatency, 200);

  // Test 4: P95 latency (10 repeated calls)
  console.log('\nTest 4: P95 latency (10 repeated calls)');
  const latencies: number[] = [];
  if (mockLatencies && typeof mockLatencies['contract:API latency (p95)'] === 'number') {
    latencies.push(mockLatencies['contract:API latency (p95)']);
  } else {
    for (let i = 0; i < 10; i++) {
      const lat = await time(`  Call ${i + 1}`, `call-${i}`, async () => {
        const url = new URL(`${apiUrl}/v1/events`);
        url.searchParams.set('contractId', testContractId);
        url.searchParams.set('limit', '25');
        const res = await fetch(url.toString(), {
          headers: apiKey ? { 'X-API-Key': apiKey } : {},
        });
        if (!res.ok) throw new Error(`API ${res.status}`);
        await res.json();
      });
      latencies.push(lat);
    }
  }
  const p95 = latencies.sort((a, b) => a - b)[Math.floor(latencies.length * 0.95)] || latencies[0];
  checkBudget('contract', 'API latency (p95)', p95, 800);

  // If requested, write current run as the baseline
  if (shouldRecordBaseline) {
    const newBaseline: BaselineFile = {
      version: '1.0.0',
      recordedAt: new Date().toISOString(),
      dataset: 'testnet (10,000+ events per contract)',
      metrics: {},
    };
    for (const r of results) {
      newBaseline.metrics[`${r.route}:${r.metric}`] = Number(r.value.toFixed(2));
    }
    fs.writeFileSync(baselineFilePath, JSON.stringify(newBaseline, null, 2) + '\n', 'utf8');
    console.log(`\n💾 Saved baseline measurements to ${baselineFilePath}`);
  }

  // Summary
  console.log('\n📋 Summary\n');
  const passed = results.filter((r) => r.ok).length;
  const total = results.length;
  const symbol = passed === total ? '✅' : '⚠️ ';
  console.log(`${symbol} ${passed}/${total} checks passed\n`);

  if (passed < total) {
    console.log('Failed targets / regressions:');
    results
      .filter((r) => !r.ok)
      .forEach((r) => {
        if (r.value > r.budget) {
          const over = r.value - r.budget;
          console.log(`  - [BUDGET] ${r.route}/${r.metric}: ${over.toFixed(2)}${r.unit} over budget (${r.value.toFixed(2)} > ${r.budget})`);
        }
        if (r.regressionOk === false && r.regressionPct !== undefined && r.baseline !== undefined) {
          console.log(`  - [REGRESSION] ${r.route}/${r.metric}: +${r.regressionPct.toFixed(1)}% regression vs baseline (${r.value.toFixed(2)} > baseline ${r.baseline} * ${(1 + regressionThreshold / 100).toFixed(2)})`);
        }
      });
    process.exit(1);
  }
}

runTests().catch((err) => {
  console.error('\n❌ Test error:', err.message);
  process.exit(1);
});
