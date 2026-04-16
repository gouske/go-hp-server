// k6/scenarios/soak.js - 장시간 안정성 테스트
// 목적: 메모리 누수, goroutine 누수, 성능 저하 감지 (30 VU, 30분)

import { sleep } from 'k6';
import { slo, assertOK, BASE_URL } from '../helpers/common.js';
import http from 'k6/http';
import { Trend } from 'k6/metrics';

// 시간대별 성능 추적용 커스텀 메트릭
const phaseLatency = new Trend('phase_latency');

export const options = {
  stages: [
    { duration: '2m',  target: 30 },  // 램프업
    { duration: '26m', target: 30 },  // 장시간 안정 부하
    { duration: '2m',  target: 0 },   // 쿨다운
  ],
  thresholds: {
    ...slo.thresholds,
    // soak에서는 성능 저하 감지가 목적이므로 시작과 끝 비교는 수동으로
    http_req_duration: ['p(50)<10', 'p(99)<100'],
  },
};

export default function () {
  const start = Date.now();
  const res = http.get(`${BASE_URL}/health`);
  
  phaseLatency.add(Date.now() - start);
  assertOK(res, 'soak-health');
  
  sleep(1);
}

export function handleSummary(data) {
  const p99Start = data.metrics?.http_req_duration?.values?.['p(99)'] || 0;
  console.log(`\n📊 Soak Test 완료`);
  console.log(`   P99 전체: ${p99Start.toFixed(2)}ms`);
  console.log(`   메모리 누수나 성능 저하 감지 필요 시 pprof 확인`);
  console.log(`   go tool pprof http://localhost:6060/debug/pprof/heap`);
  
  return {
    stdout: JSON.stringify(data, null, 2),
  };
}
