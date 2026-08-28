/**
 * Steady rare-batch load: 1 000 product users × 7 clients, one cycle every 5 minutes.
 * 7 000 client iterations / 300 s ≈ 23.33 cycles per second (ROUND_1_TRIAD_PLAN §7).
 *
 * One k6 VU = one product user; seven clients run sequentially inside the VU so
 * k6 memory does not require 7 000 VUs.
 *
 * VU start is staggered across the cycle length so all users do not hit SQLite at t=0.
 *
 * SMOKE=1 uses 100 VUs, a 20 s cycle, and 2 m duration — harness check only; those
 * numbers must not be copied into docs/LOAD.md as "measured at 1 000".
 *
 * CLI flags --vus and --duration do not override options.scenarios; scale via __ENV.
 */

import http from 'k6/http';
import { check, sleep } from 'k6';
import { SharedArray } from 'k6/data';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const SMOKE = __ENV.SMOKE === '1';
const USERS = Number(__ENV.USERS || (SMOKE ? 100 : 1000));
const CYCLE_SECONDS = Number(__ENV.CYCLE_SECONDS || (SMOKE ? 20 : 300));
const DURATION = __ENV.DURATION || (SMOKE ? '2m' : '10m');

if (!Number.isInteger(USERS) || USERS < 1) {
  throw new Error(`USERS must be an integer >= 1, got ${USERS}`);
}

const tokens = new SharedArray('tokens', () => {
  const list = JSON.parse(open('tokens.json'));
  if (list.length < 1000) {
    throw new Error(
      'load/tokens.json has fewer than 1000 tokens; rerun: go run ./load/gentokens -n 1000 -out load',
    );
  }
  return list;
});

export const options = {
  scenarios: {
    steady: {
      executor: 'constant-vus',
      vus: USERS,
      duration: DURATION,
      gracefulStop: '30s',
      exec: 'steadyUser',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.001'],
    checks: ['rate==1.0'],
  },
};

export function steadyUser() {
  const userIdx = __VU - 1;
  if (userIdx >= USERS) {
    return;
  }

  // Spread the first iteration across the cycle so the herd does not start together.
  if (__ITER === 0) {
    sleep(userIdx * (CYCLE_SECONDS / USERS));
  }

  const token = tokens[userIdx];
  const headers = {
    Authorization: `Bearer ${token}`,
    'Content-Type': 'application/json',
  };

  // One cursor per client device (seven per user).
  const cursors = [0, 0, 0, 0, 0, 0, 0];

  for (let client = 0; client < 7; client++) {
    const pullRes = http.get(
      `${BASE}/v1/sync/pull?since=${cursors[client]}&limit=100`,
      { headers, tags: { name: 'pull' } },
    );
    const pullOk = check(pullRes, {
      'pull status 200': (r) => r.status === 200,
      'pull has envelopes': (r) => {
        try {
          return Array.isArray(r.json('envelopes'));
        } catch (_) {
          return false;
        }
      },
      'pull has next_cursor': (r) => {
        try {
          return r.json('next_cursor') !== undefined;
        } catch (_) {
          return false;
        }
      },
    });
    if (pullOk) {
      cursors[client] = pullRes.json('next_cursor');
    }

    if (Math.random() < 0.25) {
      const env = {
        id: `u${userIdx}-c${client}-i${__ITER}-${Date.now()}`,
        part: 'full',
        entity_type: 'counter_operation',
        created_at_ms: Date.now(),
        last_edited_at_ms: Date.now(),
        revision: 1,
        source_id: `load-dev-${client}`,
        flags: 0,
        schema_version: 1,
        payload_encoding: 'json',
        payload: 'eyJ0eXBlIjoiaW5jcmVtZW50In0=',
      };
      const pushRes = http.post(
        `${BASE}/v1/sync/push`,
        JSON.stringify({ envelopes: [env] }),
        { headers, tags: { name: 'push' } },
      );
      check(pushRes, {
        'push status 200': (r) => r.status === 200,
        'push has results': (r) => {
          try {
            return Array.isArray(r.json('results'));
          } catch (_) {
            return false;
          }
        },
      });
    }
  }

  sleep(CYCLE_SECONDS);
}
