import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Alert, Button, Card, Descriptions, Empty, Space, Spin,
  Table, Tag, Typography, message,
} from 'antd';
import { ArrowLeftOutlined, DownloadOutlined } from '@ant-design/icons';
import * as echarts from 'echarts';
import {
  cancelRun, fmt, formatTime, getMetrics, getReport, getRun, getSnapshot, getSystem,
  isActive, MetricPoint, MetricRow, Report, RunDetail as RunDetailType,
  Snapshot, statusColor, statusText, SystemSample, reportCsvUrl,
} from '../api';

const { Title, Text } = Typography;

const AGG_ORDER = ['min', 'avg', 'p50', 'p75', 'p90', 'p95', 'p99', 'max', 'value', 'count'];

function useChart() {
  const ref = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  useEffect(() => {
    if (ref.current) {
      chart.current = echarts.init(ref.current, 'dark');
      const onResize = () => chart.current?.resize();
      window.addEventListener('resize', onResize);
      return () => {
        window.removeEventListener('resize', onResize);
        chart.current?.dispose();
        chart.current = null;
      };
    }
  }, []);
  return { ref, chart };
}

interface MetricGroup {
  key: string;
  metric_id: string;
  name: string;
  unit: string;
  aggs: Record<string, number>;
}

export default function RunDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [run, setRun] = useState<RunDetailType | null>(null);
  const [report, setReport] = useState<Report | null>(null);
  const [points, setPoints] = useState<MetricPoint[]>([]);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [system, setSystem] = useState<SystemSample[]>([]);
  const [loading, setLoading] = useState(true);
  const rate = useChart();
  const latency = useChart();
  const sys = useChart();

  const load = useCallback(async () => {
    if (!id) return;
    try {
      const r = await getRun(id);
      setRun(r);
      const [pts, rep, snap, sysSamples] = await Promise.all([
        getMetrics(id).catch(() => [] as MetricPoint[]),
        getReport(id).catch(() => null),
        getSnapshot(id).catch(() => null),
        getSystem(id).catch(() => [] as SystemSample[]),
      ]);
      setPoints(pts);
      setReport(rep);
      setSnapshot(snap);
      setSystem(sysSamples);
    } catch (e: any) {
      message.error('加载失败：' + (e?.response?.data?.error ?? e?.message ?? e));
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    if (!run || !isActive(run.status)) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [run, load]);

  // 指标总表：按 metric_id 分组，列取数据中出现的聚合
  const { groups, aggCols } = useMemo(() => {
    const map = new Map<string, MetricGroup>();
    const rows = report?.metrics ?? [];
    for (const m of rows) {
      let g = map.get(m.metric_id);
      if (!g) {
        g = { key: m.metric_id, metric_id: m.metric_id, name: m.name, unit: m.unit, aggs: {} };
        map.set(m.metric_id, g);
      }
      g.aggs[m.aggregation] = m.value;
    }
    const groups = [...map.values()];
    const aggCols = AGG_ORDER.filter((a) => rows.some((m) => m.aggregation === a));
    return { groups, aggCols };
  }, [report]);

  // 吞吐曲线
  useEffect(() => {
    const c = rate.chart.current;
    if (!c || points.length === 0) return;
    const ts = points.map((p) => formatTime(p.ts));
    c.setOption({
      backgroundColor: 'transparent',
      tooltip: { trigger: 'axis' },
      legend: { data: ['RPS', 'TPS'], textStyle: { color: '#ccc' } },
      grid: { left: 48, right: 16, top: 36, bottom: 32 },
      xAxis: { type: 'category', data: ts, axisLabel: { color: '#999', fontSize: 10 } },
      yAxis: { type: 'value', axisLabel: { color: '#999' }, splitLine: { lineStyle: { color: '#333' } } },
      series: [
        { name: 'RPS', type: 'line', smooth: true, showSymbol: false, data: points.map((p) => p.rps), lineStyle: { color: '#1677ff' } },
        { name: 'TPS', type: 'line', smooth: true, showSymbol: false, data: points.map((p) => p.tps), lineStyle: { color: '#52c41a' } },
      ],
    });
  }, [points]);

  // 延迟曲线
  useEffect(() => {
    const c = latency.chart.current;
    if (!c || points.length === 0) return;
    const ts = points.map((p) => formatTime(p.ts));
    c.setOption({
      backgroundColor: 'transparent',
      tooltip: { trigger: 'axis' },
      legend: { data: ['TTFT P50', 'TPOT P50', 'P99'], textStyle: { color: '#ccc' } },
      grid: { left: 56, right: 16, top: 36, bottom: 32 },
      xAxis: { type: 'category', data: ts, axisLabel: { color: '#999', fontSize: 10 } },
      yAxis: { type: 'value', name: 'ms', axisLabel: { color: '#999' }, splitLine: { lineStyle: { color: '#333' } } },
      series: [
        { name: 'TTFT P50', type: 'line', smooth: true, showSymbol: false, data: points.map((p) => p.ttft_p50_ms), lineStyle: { color: '#faad14' } },
        { name: 'TPOT P50', type: 'line', smooth: true, showSymbol: false, data: points.map((p) => p.tpot_p50_ms), lineStyle: { color: '#13c2c2' } },
        { name: 'P99', type: 'line', smooth: true, showSymbol: false, data: points.map((p) => p.p99_ms), lineStyle: { color: '#f5222d' } },
      ],
    });
  }, [points]);

  // 系统采集曲线（CPU / 内存双轴）
  useEffect(() => {
    const c = sys.chart.current;
    if (!c || system.length === 0) return;
    const ts = system.map((s) => formatTime(s.ts));
    c.setOption({
      backgroundColor: 'transparent',
      tooltip: { trigger: 'axis' },
      legend: { data: ['CPU 使用率', '内存使用率'], textStyle: { color: '#ccc' } },
      grid: { left: 48, right: 48, top: 36, bottom: 32 },
      xAxis: { type: 'category', data: ts, axisLabel: { color: '#999', fontSize: 10 } },
      yAxis: [
        { type: 'value', name: 'CPU %', min: 0, max: 100, axisLabel: { color: '#999' }, splitLine: { lineStyle: { color: '#333' } } },
        { type: 'value', name: '内存 %', min: 0, max: 100, axisLabel: { color: '#999' }, splitLine: { show: false } },
      ],
      series: [
        { name: 'CPU 使用率', type: 'line', smooth: true, showSymbol: false, data: system.map((s) => s.cpu_util_pct), lineStyle: { color: '#ff7a45' } },
        { name: '内存使用率', type: 'line', smooth: true, showSymbol: false, yAxisIndex: 1, data: system.map((s) => s.mem_used_pct), lineStyle: { color: '#722ed1' } },
      ],
    });
  }, [system]);

  const onCancel = async () => {
    if (!id) return;
    try {
      await cancelRun(id);
      message.success('已取消');
      load();
    } catch (e: any) {
      message.error('取消失败：' + (e?.response?.data?.error ?? e?.message ?? e));
    }
  };

  if (loading || !run) {
    return <Spin size="large" style={{ display: 'block', margin: '80px auto' }} />;
  }

  const active = isActive(run.status);
  const slo = report?.slo;
  const hw = snapshot?.hardware ?? {};

  return (
    <div>
      <Space style={{ marginBottom: 16 }} wrap>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/')}>返回列表</Button>
        <Title level={3} style={{ margin: 0 }}>{run.name}</Title>
        <Tag color={statusColor[run.status] ?? 'default'}>{statusText[run.status] ?? run.status}</Tag>
        {slo && (
          <Tag color={slo.passed ? 'success' : 'error'}>
            SLO {slo.passed ? '通过' : '未通过'}
          </Tag>
        )}
      </Space>

      {run.error && <Alert type="error" message={run.error} style={{ marginBottom: 16 }} />}
      {slo && !slo.passed && slo.violations && slo.violations.length > 0 && (
        <Alert
          type="warning"
          message="SLO 违例"
          description={<ul style={{ margin: 0, paddingLeft: 18 }}>{slo.violations.map((v, i) => <li key={i}>{v}</li>)}</ul>}
          style={{ marginBottom: 16 }}
        />
      )}

      <Card style={{ marginBottom: 16 }}>
        <Descriptions column={4} size="small">
          <Descriptions.Item label="引擎">{run.engine}</Descriptions.Item>
          <Descriptions.Item label="创建时间">{formatTime(run.created_at)}</Descriptions.Item>
          <Descriptions.Item label="开始时间">{formatTime(run.started_at)}</Descriptions.Item>
          <Descriptions.Item label="结束时间">{formatTime(run.finished_at)}</Descriptions.Item>
        </Descriptions>
        <Space style={{ marginTop: 12 }}>
          {active && <Button danger onClick={onCancel}>取消运行</Button>}
          {report && (
            <Button icon={<DownloadOutlined />} href={reportCsvUrl(run.id)} target="_blank">
              下载 CSV 报告
            </Button>
          )}
        </Space>
      </Card>

      <Card title="指标总表" style={{ marginBottom: 16 }}>
        {groups.length === 0 ? (
          <Empty description="暂无指标数据" />
        ) : (
          <Table<MetricGroup>
            rowKey="key"
            dataSource={groups}
            pagination={false}
            size="small"
            scroll={{ x: true }}
            columns={[
              {
                title: '指标', key: 'name', fixed: 'left', width: 220,
                render: (_, g: MetricGroup) => (
                  <div>
                    <div>{g.name}</div>
                    <Text type="secondary" style={{ fontSize: 11 }}>{g.metric_id}</Text>
                  </div>
                ),
              },
              { title: '单位', dataIndex: 'unit', key: 'unit', width: 80 },
              ...aggCols.map((a) => ({
                title: a.toUpperCase(), key: a, width: 110,
                render: (_: unknown, g: MetricGroup) => fmt(g.aggs[a]),
              })),
            ]}
          />
        )}
      </Card>

      <Card title="吞吐量（RPS / TPS）" style={{ marginBottom: 16 }}>
        {points.length === 0 ? <Empty description="暂无采样数据" /> : <div ref={rate.ref} style={{ height: 300 }} />}
      </Card>

      <Card title="延迟（ms）" style={{ marginBottom: 16 }}>
        {points.length === 0 ? <Empty description="暂无采样数据" /> : <div ref={latency.ref} style={{ height: 300 }} />}
      </Card>

      <Card title="系统采集（CPU / 内存）" style={{ marginBottom: 16 }}>
        {system.length === 0 ? (
          <Empty description="暂无系统采集数据（需启用 collector）" />
        ) : (
          <div ref={sys.ref} style={{ height: 300 }} />
        )}
      </Card>

      <Card title="运行快照">
        {snapshot ? (
          <Descriptions column={4} size="small" bordered>
            <Descriptions.Item label="引擎">{snapshot.engine}</Descriptions.Item>
            <Descriptions.Item label="引擎版本">{snapshot.engine_version || '-'}</Descriptions.Item>
            <Descriptions.Item label="模型">{snapshot.model}</Descriptions.Item>
            <Descriptions.Item label="协议">{snapshot.protocol}</Descriptions.Item>
            <Descriptions.Item label="数据集">{snapshot.dataset_type}</Descriptions.Item>
            <Descriptions.Item label="随机种子">{snapshot.seed}</Descriptions.Item>
            <Descriptions.Item label="输入 token">{snapshot.input_tokens}</Descriptions.Item>
            <Descriptions.Item label="输出 token">{snapshot.output_tokens}</Descriptions.Item>
            {Object.entries(hw).map(([k, v]) => (
              <Descriptions.Item key={k} label={`硬件·${k}`}>{String(v)}</Descriptions.Item>
            ))}
          </Descriptions>
        ) : (
          <Empty description="暂无快照" />
        )}
      </Card>
    </div>
  );
}
