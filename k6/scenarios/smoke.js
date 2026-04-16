// k6/scenarios/smoke.js - 최소 부하, 기본 동작 확인
// 목적: 배포 후 기본 동작 검증 (1 VU, 1분)

import { sleep } from 'k6';
import { slo, healthCheck, assertOK, BASE_URL } from '../helpers/common.js';
import http from 'k6/http';

export const options = {
  vus: 1,
  duration: '1m',
  thresholds: slo.thresholds,
};

export default function () {
  healthCheck();
  
  const res = http.get(`${BASE_URL}/health`);
  assertOK(res, 'smoke-health');
  
  sleep(1);
}
