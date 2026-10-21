import { execFileSync } from 'child_process';
import * as fs from 'fs';
import * as path from 'path';
import * as assert from 'assert';

const explorerDir = path.resolve(import.meta.dirname, '..');
const scriptPath = path.resolve(explorerDir, 'scripts/perf-test.ts');
const tempBaseline = path.resolve(explorerDir, 'test-baseline.json');

console.log('Testing explorer performance budget runner and regression detection...');

try {
  // Setup temp baseline
  const initialBaseline = {
    version: '1.0.0',
    recordedAt: '2026-10-21T11:00:00.000Z',
    dataset: 'testnet (10,000+ events per contract)',
    metrics: {
      'home:API latency': 100.0,
      'contract:API latency': 200.0,
      'event-detail:API latency': 80.0,
      'contract:API latency (p95)': 300.0,
    },
  };
  fs.writeFileSync(tempBaseline, JSON.stringify(initialBaseline, null, 2), 'utf8');

  // Test 1: Normal pass (within +10% regression and under budget)
  console.log('\nCase 1: Normal measurements within baseline and budget');
  const normalLatencies = JSON.stringify({
    'home:API latency': 105.0, // +5% vs 100
    'contract:API latency': 210.0, // +5% vs 200
    'event-detail:API latency': 82.0, // +2.5% vs 80
    'contract:API latency (p95)': 315.0, // +5% vs 300
  });

  const out1 = execFileSync(process.execPath, [
    '--experimental-strip-types',
    scriptPath,
    '--baseline-file', tempBaseline,
    '--regression-threshold', '10',
    '--mock-latencies', normalLatencies,
  ], { cwd: explorerDir, encoding: 'utf8' });

  assert.ok(out1.includes('4/4 checks passed'), 'Expected 4/4 checks to pass');
  console.log('✓ Normal run passed as expected');

  // Test 2: Deliberate 20% regression triggers failure
  console.log('\nCase 2: Deliberate 20% regression triggers failure');
  const regressedLatencies = JSON.stringify({
    'home:API latency': 122.0, // +22% vs 100 -> REGRESSION!
    'contract:API latency': 200.0,
    'event-detail:API latency': 80.0,
    'contract:API latency (p95)': 300.0,
  });

  let failedAsExpected = false;
  try {
    execFileSync(process.execPath, [
      '--experimental-strip-types',
      scriptPath,
      '--baseline-file', tempBaseline,
      '--regression-threshold', '10',
      '--mock-latencies', regressedLatencies,
    ], { cwd: explorerDir, encoding: 'utf8' });
  } catch (err) {
    failedAsExpected = true;
    const stdout = err.stdout || '';
    assert.ok(stdout.includes('[REGRESSION]'), 'Expected regression report in stdout');
    assert.ok(stdout.includes('+22.0% regression vs baseline'), 'Expected +22.0% regression logged');
  }
  assert.ok(failedAsExpected, 'Expected 20% regression to exit with non-zero code');
  console.log('✓ 20% regression correctly failed and reported violation');

  // Test 3: Record baseline writes committed file
  console.log('\nCase 3: Record baseline updates file');
  const newBaselineOutput = path.resolve(explorerDir, 'test-new-baseline.json');
  const recordLatencies = JSON.stringify({
    'home:API latency': 115.0,
    'contract:API latency': 225.0,
    'event-detail:API latency': 82.0,
    'contract:API latency (p95)': 350.0,
  });

  execFileSync(process.execPath, [
    '--experimental-strip-types',
    scriptPath,
    '--baseline-file', newBaselineOutput,
    '--record-baseline',
    '--mock-latencies', recordLatencies,
  ], { cwd: explorerDir, encoding: 'utf8' });

  assert.ok(fs.existsSync(newBaselineOutput), 'Expected new baseline file to be created');
  const saved = JSON.parse(fs.readFileSync(newBaselineOutput, 'utf8'));
  assert.strictEqual(saved.metrics['home:API latency'], 115.0);
  assert.strictEqual(saved.metrics['contract:API latency (p95)'], 350.0);
  fs.unlinkSync(newBaselineOutput);
  console.log('✓ Baseline recording works and preserves JSON format');

  console.log('\n🎉 All performance testing and regression detection checks PASSED!');
} finally {
  if (fs.existsSync(tempBaseline)) {
    fs.unlinkSync(tempBaseline);
  }
}
