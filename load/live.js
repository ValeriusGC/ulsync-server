/**
 * Live correctness under load: 100 users × 7 SSE connections (700 VU) plus one pusher.
 *
 * xk6-sse blocks the VU for the entire connection lifetime, so this script checks
 * per-user isolation and wakeup latency, not 70 000-connection capacity.
 * Community extension k6/x/sse — requires load/k6-sse binary (see docs/LOAD.md).
 *
 * Default 100×7 is the acceptance scale, not a reduced substitute for 1 000×7.
 * SMOKE=1 → 10×7 = 70 VU to prove the extension loads; do not copy into LOAD.md.
 *
 * SSE comment heartbeats arrive as event.comment containing "ping" (server writes ": ping\n\n").
 */

import http from 'k6/http';
import sse from 'k6/x/sse';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { SharedArray } from 'k6/data';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const SMOKE = __ENV.SMOKE === '1';
const LIVE_USERS = Number(__ENV.LIVE_USERS || (SMOKE ? 10 : 100));

if (!Number.isInteger(LIVE_USERS) || LIVE_USERS < 1) {
  throw new Error(`LIVE_USERS must be an integer >= 1, got ${LIVE_USERS}`);
}

const foreignEnvelopes = new Counter('foreign_envelopes');

const tokens = new SharedArray('tokens', () => {
  const list = JSON.parse(open('tokens.json'));
  if (list.length < LIVE_USERS) {
    throw new Error(
      `tokens.json has ${list.length} tokens, need at least ${LIVE_USERS}; rerun gentokens`,
    );
  }
  return list;
});

export const options = {
  scenarios: {
    receivers: {
      executor: 'constant-vus',
      vus: LIVE_USERS * 7,
      duration: '45s',
      exec: 'receive',
    },
    pusher: {
      executor: 'constant-vus',
      vus: 1,
      duration: '25s',
      startTime: '20s',
      exec: 'pushOne',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.001'],
    checks: ['rate==1.0'],
    foreign_envelopes: ['count==0'],
  },
};

export function receive() {
  const userIdx = Math.floor((__VU - 1) / 7);
  const token = tokens[userIdx];
  const params = {
    headers: { Authorization: `Bearer ${token}` },
    tags: { name: 'sse' },
  };

  let pings = 0;
  let gotEnvelope = false;
  let envelopeAt = 0;
  const started = Date.now();

  const response = sse.open(`${BASE}/v1/sync/pull?since=0&live=sse`, params, (client) => {
    client.on('event', (event) => {
      // xk6-sse: named events use event.name; SSE comments use event.comment.
      if (event.name === 'envelope') {
        if (userIdx === 0) {
          gotEnvelope = true;
          envelopeAt = Date.now();
        } else {
          foreignEnvelopes.add(1);
        }
      }
      const comment = event.comment || '';
      if (comment.includes('ping')) {
        pings += 1;
      }
    });
  });

  check(response, { 'sse status 200': (r) => r && r.status === 200 });

  if (userIdx === 0) {
    check(null, {
      'user0 received envelope before heartbeat window': () => gotEnvelope && envelopeAt - started < 15000,
      'user0 wakeup within 5s of pusher start': () => gotEnvelope && envelopeAt - started < 25000,
    });
  } else {
    check(null, {
      'silent user received heartbeats': () => pings >= 2,
    });
  }
}

export function pushOne() {
  const token = tokens[0];
  const headers = {
    Authorization: `Bearer ${token}`,
    'Content-Type': 'application/json',
  };
  const env = {
    id: `live-push-${Date.now()}-${__ITER}`,
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
    tags: { name: 'push' },
  });
  check(res, {
    'pusher status 200': (r) => r.status === 200,
    'pusher applied': (r) => {
      try {
        return r.json('results.0.applied') === true;
      } catch (_) {
        return false;
      }
    },
  });
}
