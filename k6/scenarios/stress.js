// k6/scenarios/stress.js - 한계 부하 테스트
// 목적: 서버가 버티는 최대 부하 찾기 (최대 200 VU, 10분)

import { sleep } from 'k6';
import { slo, assertOK, BASE_URL } from '../helpers/common.js';
import http from 'k6/http';

export const options = {
  stages: [
    { duration: '2m', target: 50 },
    { duration: '2m', target: 100 },
    { duration: '2m', target: 150 },
    { duration: '2m', target: 200 },
    { duration: '2m', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(99)<200'],  // stress에서는 목표 완화
    http_req_failed: ['rate<0.05'],    // 5% 이하 허용
  },
};

export default function () {
  const res = http.get(`${BASE_URL}/health`);
  assertOK(res, 'stress-health');
  sleep(0.1);
}
