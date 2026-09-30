import axios from 'axios';

const api = axios.create({ baseURL: '/api/v1', timeout: 30000 });

export type RunStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled';

export interface Run {
  id: string;
  name: string;
  engine: string;
  status: RunStatus;
  error?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

export interface RunDetail extends Run {
  spec?: Record<string, any>;
}

export interface MetricPoint {
  run_id: string;
  ts: string;
  rps: number;
  tps: number;
  ttft_p50_ms: number;
  tpot_p50_ms: number;
  p99_ms: number;
}

export interface SloResult {
  passed: boolean;
  violations?: string[];
}

export interface Report {
  run_id: string;
  total_requests: number;
  success_count: number;
  fail_count: number;
  duration_sec: number;
  rps: number;
  tps: number;
  ttft_p50_ms: number;
  ttft_p99_ms: number;
  tpot_p50_ms: number;
  tpot_p99_ms: number;
  e2e_p50_ms: number;
  e2e_p99_ms: number;
  output_tokens: number;
  slo?: SloResult;
}

export interface Spec {
  name: string;
  target: { base_url: string; model: string; api_key?: string };
  engine: string;
  workload: {
    concurrency: number;
    requests: number;
    input_tokens: number;
    output_tokens: number;
    stream?: boolean;
  };
  slo?: { p99_latency_ms?: number; min_tps?: number; max_ttft_ms?: number };
}

export async function listRuns(): Promise<Run[]> {
  const r = await api.get('/runs');
  return r.data.runs ?? [];
}

export async function getRun(id: string): Promise<RunDetail> {
  const r = await api.get(`/runs/${id}`);
  return r.data;
}

export async function createRun(spec: Spec): Promise<string> {
  const r = await api.post('/runs', spec);
  const id = r.data.id ?? r.data.run_id;
  if (!id) throw new Error('后端未返回运行 ID');
  return id;
}

export async function cancelRun(id: string): Promise<void> {
  await api.post(`/runs/${id}/cancel`);
}

export async function getMetrics(id: string): Promise<MetricPoint[]> {
  const r = await api.get(`/runs/${id}/metrics`);
  return r.data.points ?? [];
}

export async function getReport(id: string): Promise<Report> {
  const r = await api.get(`/runs/${id}/report`);
  return r.data;
}

export async function listEngines(): Promise<string[]> {
  const r = await api.get('/engines');
  return r.data.engines ?? [];
}

export function reportCsvUrl(id: string): string {
  return `/api/v1/runs/${id}/report?format=csv`;
}

export function isActive(status: RunStatus): boolean {
  return status === 'pending' || status === 'running';
}

export function formatTime(s?: string): string {
  if (!s) return '-';
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString('zh-CN', { hour12: false });
}
