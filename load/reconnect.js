/**
 * Thundering herd: many clients pull at once after a simulated network return or
 * server restart. Latency is recorded by k6 but is not a pass/fail threshold.
 * Pass = process alive and every client sees its seed envelope after cursor walk.
 *
 * SMOKE=1 → 100 users × 7 = 700 VU. Default 7 000 VU = 1 000 × 7.
 * CLI --vus does not override options.scenarios; use SMOKE or USERS env.
 */

import http from 'k6/http';
import { check, sleep } from 'k6';
import { SharedArray } from 'k6/data';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const SMOKE = __ENV.SMOKE === '1';
const USERS = Number(__ENV.USERS || (SMOKE ? 100 : 1000));
const CLIENTS = 7;
const HERD = USERS * CLIENTS;

if (!Number.isInteger(USERS) || USERS < 1) {
  throw new Error(`USERS must be an integer >= 1, got ${USERS}`);
}

const tokens = new SharedArray('tokens', () => {
  const list = JSON.parse(open('tokens.json'));
  if (list.length < USERS) {
    throw new Error(`need at least ${USERS} tokens in load/tokens.json`);
  }
  return list;
});

export function setup() {
  for (let u = 0; u < USERS; u++) {
    const token = tokens[u];
    const headers = {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    };
    const env = {
      id: `seed-${u}`,
      part: 'full',
      entity_type: 'counter_operation',
      created_at_ms: Date.now(),
      last_edited_at_ms: Date.now(),
      revision: 1,
      source_id: 'load-dev-0',
      flags: 0,
      schema_version: 1,
      payload_encoding: 'json',
      payload: 'eyJ0eXBlIjoiaW5jcmVtZW50In0=',
    };
    const res = http.post(`${BASE}/v1/sync/push`, JSON.stringify({ envelopes: [env] }), {
      headers,
      tags: { name: 'setup-push' },
    });
    if (res.status !== 200) {
      throw new Error(`setup push for user ${u} failed: ${res.status} ${res.body}`);
    }
  }
}

export const options = {
  scenarios: {
    herd: {
      executor: 'shared-iterations',
      vus: HERD,
      iterations: HERD,
      maxDuration: SMOKE ? '1m' : '2m',
      exec: 'herdPull',
    },
  },
  thresholds: {
    checks: ['rate==1.0'],
  },
};

export function herdPull() {
  const id = __VU - 1;
  const userIdx = Math.floor(id / CLIENTS);
  if (userIdx >= USERS) {
    return;
  }

  const token = tokens[userIdx];
  const headers = { Authorization: `Bearer ${token}` };
  const wantID = `seed-${userIdx}`;
  const seen = new Set();
  let since = 0;

  for (let page = 0; page < 1000; page++) {
    let res = null;
    for (let attempt = 0; attempt < 10; attempt++) {
      res = http.get(`${BASE}/v1/sync/pull?since=${since}&limit=100`, {
        headers,
        tags: { name: 'pull' },
      });
      if (res.status === 503) {
        sleep(0.05);
        continue;
      }
      break;
    }
    if (!res || res.status !== 200) {
      check(null, { 'pull status 200': () => false });
      return;
    }

    const body = res.json();
    const envelopes = body.envelopes || [];
    for (const env of envelopes) {
      seen.add(env.id);
    }
    if (seen.has(wantID)) {
      break;
    }
    if (envelopes.length === 0) {
      break;
    }
    since = body.next_cursor;
  }

  check(null, {
    [`found ${wantID}`]: () => seen.has(wantID),
  });
}
