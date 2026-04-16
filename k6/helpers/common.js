// k6/helpers/common.js - 공통 유틸리티

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

// 커스텀 메트릭
export const customDuration = new Trend('custom_duration');
export const errorRate = new Rate('errors');
export const requestCount = new Counter('request_count');

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// 공통 헤더
export const defaultHeaders = {
  'Content-Type': 'application/json',
  'Accept': 'application/json',
};

// 헬스체크
export function healthCheck() {
  const res = http.get(`${BASE_URL}/health`, { headers: defaultHeaders });
  return check(res, {
    'health: status 200': (r) => r.status === 200,
  });
}

// 성능 목표 (SLO) - PIPELINE.md 기준
export const slo = {
  thresholds: {
    http_req_duration: ['p(50)<5', 'p(99)<50'],  // ms
    http_req_failed: ['rate<0.001'],              // 0.1%
    errors: ['rate<0.001'],
  },
};

// 요청 성공 체크
export function assertOK(res, name) {
  const ok = check(res, {
    [`${name}: status 2xx`]: (r) => r.status >= 200 && r.status < 300,
    [`${name}: has body`]: (r) => r.body && r.body.length > 0,
    [`${name}: latency < 100ms`]: (r) => r.timings.duration < 100,
  });
  if (!ok) errorRate.add(1);
  requestCount.add(1);
  return ok;
}

export { BASE_URL };
