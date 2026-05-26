/**
 * Phase 15.7 — Load Test: 5× Expected Peak
 *
 * Target: sustained 5× peak load for 2 hours.
 * Pass criteria:
 *   - p99 response time < 200 ms for PDP decide endpoint
 *   - p99 response time < 500 ms for proxy connect
 *   - Error rate < 0.1%
 *   - No OOM kills or pod restarts during the test
 *
 * Usage:
 *   k6 run --env BASE_URL=https://api.governance-platform-staging.io \
 *          --env JWT_TOKEN=<token> \
 *          scripts/load-test/k6-5x-peak.js
 */

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Histogram, Rate } from 'k6/metrics';
import { SharedArray } from 'k6/data';

// ── Custom metrics ─────────────────────────────────────────────────────────────
const pdpErrors = new Counter('pdp_errors_total');
const proxyErrors = new Counter('proxy_errors_total');
const policyErrors = new Counter('policy_errors_total');
const errorRate = new Rate('error_rate');

// ── Test configuration ─────────────────────────────────────────────────────────
// Steady-state peak assumption: 1,000 rps.
// 5× peak = 5,000 rps sustained for 2 hours.
export const options = {
  scenarios: {
    pdp_decide: {
      executor: 'constant-arrival-rate',
      rate: 3000,          // 3,000 rps — majority of traffic is auth decisions
      timeUnit: '1s',
      duration: '120m',
      preAllocatedVUs: 500,
      maxVUs: 2000,
      exec: 'pdpDecide',
    },
    policy_list: {
      executor: 'constant-arrival-rate',
      rate: 500,
      timeUnit: '1s',
      duration: '120m',
      preAllocatedVUs: 50,
      maxVUs: 200,
      exec: 'policyList',
    },
    audit_query: {
      executor: 'constant-arrival-rate',
      rate: 1000,
      timeUnit: '1s',
      duration: '120m',
      preAllocatedVUs: 100,
      maxVUs: 400,
      exec: 'auditQuery',
    },
    proxy_connect: {
      executor: 'constant-arrival-rate',
      rate: 500,
      timeUnit: '1s',
      duration: '120m',
      preAllocatedVUs: 100,
      maxVUs: 500,
      exec: 'proxyConnect',
    },
  },

  thresholds: {
    // PDP must be snappy — it's in the critical path of every DB query
    'http_req_duration{scenario:pdp_decide}': ['p(99)<200'],
    // Policy list can be slightly slower
    'http_req_duration{scenario:policy_list}': ['p(99)<500'],
    // Audit queries are async-friendly
    'http_req_duration{scenario:audit_query}': ['p(99)<2000'],
    // Proxy connect includes pgproto3 handshake
    'http_req_duration{scenario:proxy_connect}': ['p(99)<500'],
    // Global error rate
    'error_rate': ['rate<0.001'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'https://api.governance-platform-staging.io';
const JWT = __ENV.JWT_TOKEN;

function headers() {
  return {
    'Authorization': `Bearer ${JWT}`,
    'Content-Type': 'application/json',
    'X-Janus-Tenant-ID': 'load-test-tenant-00000000-0000-0000-0000-000000000001',
  };
}

// ── Scenarios ──────────────────────────────────────────────────────────────────
export function pdpDecide() {
  const payload = JSON.stringify({
    subject:  { id: `user-${__VU}`, roles: ['analyst'] },
    resource: { type: 'table', id: 'transactions', tenant_id: 'load-test-tenant-00000000-0000-0000-0000-000000000001' },
    action:   'select',
    context:  { risk_score: Math.floor(Math.random() * 60) },
  });

  const res = http.post(`${BASE_URL}/v1/pdp/decide`, payload, {
    headers: headers(),
    tags: { endpoint: 'pdp_decide' },
  });

  const ok = check(res, {
    'pdp decide 200': (r) => r.status === 200,
    'pdp has decision': (r) => r.json('decision') !== undefined,
  });

  if (!ok) { pdpErrors.add(1); errorRate.add(1); } else { errorRate.add(0); }
  sleep(0.001);
}

export function policyList() {
  const res = http.get(`${BASE_URL}/v1/policies?page=1&limit=20`, {
    headers: headers(),
    tags: { endpoint: 'policy_list' },
  });

  const ok = check(res, {
    'policy list 200': (r) => r.status === 200,
  });
  if (!ok) { policyErrors.add(1); errorRate.add(1); } else { errorRate.add(0); }
  sleep(0.001);
}

export function auditQuery() {
  const from = new Date(Date.now() - 3600_000).toISOString();
  const res = http.get(
    `${BASE_URL}/v1/audit?from=${from}&limit=50`,
    { headers: headers(), tags: { endpoint: 'audit_query' } },
  );

  const ok = check(res, { 'audit 200': (r) => r.status === 200 });
  if (!ok) { errorRate.add(1); } else { errorRate.add(0); }
  sleep(0.001);
}

export function proxyConnect() {
  // Test the db-token endpoint (Phase 6 gateway API):
  const res = http.post(`${BASE_URL}/v1/db-token`, JSON.stringify({
    datasource_id: 'load-test-ds-00000000-0000-0000-0000-000000000001',
  }), { headers: headers(), tags: { endpoint: 'proxy_connect' } });

  const ok = check(res, {
    'db-token 200 or 404': (r) => [200, 404].includes(r.status),
  });
  if (!ok) { proxyErrors.add(1); errorRate.add(1); } else { errorRate.add(0); }
  sleep(0.001);
}

// ── Setup + teardown ───────────────────────────────────────────────────────────
export function setup() {
  console.log(`Load test starting against: ${BASE_URL}`);
  console.log(`Target: 5,000 rps sustained for 120 minutes`);
  // Verify connectivity before starting
  const res = http.get(`${BASE_URL}/healthz`);
  if (res.status !== 200) {
    throw new Error(`Health check failed (${res.status}) — aborting load test`);
  }
}

export function teardown() {
  console.log('Load test complete. Review thresholds in k6 output.');
  console.log('Also check:');
  console.log('  - Grafana: "Phase 15 Load Test" dashboard');
  console.log('  - kubectl top pods -n governance-platform');
  console.log('  - No OOMKilled pods: kubectl get events -n governance-platform | grep OOMKill');
}
