import axios from 'axios';

const api = axios.create({ baseURL: '/api/v1', timeout: 30000 });

// 运行状态机：created → preparing → warming_up → running → normalizing → analyzing → completed
export type RunStatus =
  | 'created'
  | 'preparing'
  | 'warming_up'
  | 'running'
  | 'normalizing'
  | 'analyzing'
  | 'completed'
  | 'failed'
  | 'cancelled';

export const statusText: Record<RunStatus, string> = {
  created: '已创建',
  preparing: '准备中',
  warming_up: '预热中',
  running: '运行中',
  normalizing: '归一化中',
  analyzing: '分析中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
};

export const statusColor: Record<RunStatus, string> = {
  created: 'default',
  preparing: 'processing',
  warming_up: 'processing',
  running: 'processing',
  normalizing: 'processing',
  analyzing: 'processing',
  completed: 'success',
  failed: 'error',
  cancelled: 'warning',
};

export function isActive(status: RunStatus): boolean {
  return status !== 'completed' && status !== 'failed' && status !== 'cancelled';
}

export interface EngineInfo {
  name: string;
  capabilities: Record<string, unknown>;
}

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
  spec?: Record<string, unknown>;
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

export interface MetricRow {
  metric_id: string;
  aggregation: string;
  value: number;
  unit: string;
  name: string;
}

export interface SloResult {
  passed: boolean;
  violations?: string[];
}

export interface Report {
  run_id: string;
  metrics: MetricRow[];
  slo: SloResult;
}

export interface Snapshot {
  run_id: string;
  engine: string;
  engine_version: string;
  model: string;
  protocol: string;
  dataset_type: string;
  seed: number;
  input_tokens: number;
  output_tokens: number;
  hardware: Record<string, unknown>;
  created_at: string;
}

export interface SystemSample {
  run_id: string;
  ts: string;
  cpu_util_pct: number;
  mem_used_pct: number;
  load1: number;
  net_rx_bytes: number;
  net_tx_bytes: number;
}

export interface FairnessItem {
  field: string;
  values: unknown[];
  match: boolean;
}

export interface CompareRun {
  id: string;
  name: string;
  status: RunStatus;
  snapshot: Record<string, unknown>;
  fairness: FairnessItem[];
  metrics: Record<string, Record<string, number>>;
}

export interface MetricDefinition {
  id: string;
  name: string;
  type: string;
  unit: string;
  source: string;
  scope: string;
  aggregations: string[];
}

// Benchmark Spec v2（对应后端 internal/spec.Spec）
export interface SpecV2 {
  name: string;
  target: {
    endpoint: string;
    protocol?: string;
    model: string;
    api_key?: string;
  };
  engine: { type: string };
  dataset: {
    type: string;
    input_tokens: number;
    output_tokens: number;
    seed?: number;
  };
  load: {
    mode?: string;
    concurrency: number;
    requests?: number;
    duration?: string;
    rate?: number;
    stream?: boolean;
  };
  warmup?: { duration?: string };
  thresholds?: {
    ttft_p95?: number;
    ttft_p99?: number;
    max_ttft_ms?: number;
    min_tps?: number;
    error_rate?: number;
    p99_latency_ms?: number;
  };
}

export async function listRuns(status?: string): Promise<Run[]> {
  const r = await api.get('/runs', { params: status ? { status } : {} });
  return r.data.runs ?? [];
}

export async function getRun(id: string): Promise<RunDetail> {
  const r = await api.get(`/runs/${id}`);
  return r.data;
}

export async function createRun(spec: SpecV2): Promise<string> {
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

export async function getSnapshot(id: string): Promise<Snapshot> {
  const r = await api.get(`/runs/${id}/snapshot`);
  return r.data;
}

export async function getSystem(id: string): Promise<SystemSample[]> {
  const r = await api.get(`/runs/${id}/system`);
  return r.data.samples ?? [];
}

export async function compareRuns(ids: string[]): Promise<CompareRun[]> {
  const r = await api.get('/runs/compare', { params: { ids: ids.join(',') } });
  return r.data.runs ?? [];
}

export async function listEngines(): Promise<EngineInfo[]> {
  const r = await api.get('/engines');
  return r.data.engines ?? [];
}

export async function listMetricDefinitions(): Promise<MetricDefinition[]> {
  const r = await api.get('/metrics/definitions');
  return r.data.definitions ?? [];
}

export function reportCsvUrl(id: string): string {
  return `/api/v1/runs/${id}/report?format=csv`;
}

export function formatTime(s?: string): string {
  if (!s) return '-';
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString('zh-CN', { hour12: false });
}

export function fmt(v?: number | null, digits = 2): string {
  if (v === undefined || v === null || Number.isNaN(v)) return '-';
  return Number(v).toFixed(digits);
}
