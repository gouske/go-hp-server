// k6/scenarios/load.js - 정상 부하 테스트
// 목적: 일반적인 트래픽 패턴에서 성능 측정 (50 VU, 5분)

import { sleep } from 'k6';
import { slo, assertOK, BASE_URL } from '../helpers/common.js';
import http from 'k6/http';

export const options = {
  stages: [
    { duration: '1m', target: 50 },   // 램프업
    { duration: '3m', target: 50 },   // 안정 부하
    { duration: '1m', target: 0 },    // 쿨다운
  ],
  thresholds: slo.thresholds,
};

export default function () {
  const res = http.get(`${BASE_URL}/health`);
  assertOK(res, 'load-health');
  sleep(Math.random() * 0.5);  // 0~0.5초 랜덤 대기
}
