// k6/scenarios/spike.js - 급격한 트래픽 폭증 테스트
// 목적: 갑작스러운 트래픽 급증 시 서버 반응 확인

import { sleep } from 'k6';
import { assertOK, BASE_URL } from '../helpers/common.js';
import http from 'k6/http';

export const options = {
  stages: [
    { duration: '30s', target: 10 },   // 기본 부하
    { duration: '10s', target: 500 },  // 급격한 폭증
    { duration: '1m',  target: 500 },  // 높은 부하 유지
    { duration: '10s', target: 10 },   // 급격한 감소
    { duration: '30s', target: 10 },   // 회복 확인
    { duration: '30s', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(99)<500'],
    http_req_failed: ['rate<0.1'],     // 스파이크는 10% 허용
  },
};

export default function () {
  const res = http.get(`${BASE_URL}/health`);
  assertOK(res, 'spike-health');
  sleep(0.05);
}
