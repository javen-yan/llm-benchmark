import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Alert, Button, Card, Col, Descriptions, Row, Space, Spin, Statistic, Tag, Typography, message } from 'antd';
import { ArrowLeftOutlined, DownloadOutlined } from '@ant-design/icons';
import * as echarts from 'echarts';
import {
  cancelRun, formatTime, getMetrics, getReport, getRun,
  isActive, MetricPoint, Report, RunDetail as RunDetailType, RunStatus, reportCsvUrl,
} from '../api';

const { Title } = Typography;

const statusColor: Record<string, string> = {
  pending: 'default', running: 'processing', succeeded: 'success', failed: 'error', cancelled: 'warning',
};
const statusText: Record<string, string> = {
  pending: '等待中', running: '运行中', succeeded: '成功', failed: '失败', cancelled: '已取消',
};

function fmt(v?: number, digits = 2): string {
  if (v === undefined || v === null || Number.isNaN(v)) return '-';
  return Number(v).toFixed(digits);
}

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

export default function RunDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [run, setRun] = useState<RunDetailType | null>(null);
  const [report, setReport] = useState<Report | null>(null);
  const [points, setPoints] = useState<MetricPoint[]>([]);
  const [loading, setLoading] = useState(true);
  const rate = useChart();
  const latency = useChart();

  const load = useCallback(async () => {
    if (!id) return;
    try {
      const r = await getRun(id);
      setRun(r);
      const pts = await getMetrics(id).catch(() => [] as MetricPoint[]);
      setPoints(pts);
      const rep = await getReport(id).catch(() => null);
      setReport(rep);
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
    if (!run) return;
    if (!isActive(run.status as RunStatus)) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [run?.status, load]);

  // 吞吐曲线
  useEffect(() => {
    const c = rate.chart.current;
    if (!c) return;
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
    if (!c) return;
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

  const active = isActive(run.status as RunStatus);
  const slo = report?.slo;

  return (
    <div>
      <Space style={{ marginBottom: 16 }}>
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
      {slo && !slo.passed && slo.violations && (
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

      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={6}><Card><Statistic title="RPS" value={fmt(report?.rps)} /></Card></Col>
        <Col span={6}><Card><Statistic title="TPS" value={fmt(report?.tps)} /></Card></Col>
        <Col span={6}><Card><Statistic title="成功 / 失败" value={`${report?.success_count ?? '-'} / ${report?.fail_count ?? '-'}`} /></Card></Col>
        <Col span={6}><Card><Statistic title="总耗时 (s)" value={fmt(report?.duration_sec)} /></Card></Col>
      </Row>
      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={4}><Card><Statistic title="TTFT P50 (ms)" value={fmt(report?.ttft_p50_ms)} /></Card></Col>
        <Col span={4}><Card><Statistic title="TTFT P99 (ms)" value={fmt(report?.ttft_p99_ms)} /></Card></Col>
        <Col span={4}><Card><Statistic title="TPOT P50 (ms)" value={fmt(report?.tpot_p50_ms)} /></Card></Col>
        <Col span={4}><Card><Statistic title="TPOT P99 (ms)" value={fmt(report?.tpot_p99_ms)} /></Card></Col>
        <Col span={4}><Card><Statistic title="端到端 P50 (ms)" value={fmt(report?.e2e_p50_ms)} /></Card></Col>
        <Col span={4}><Card><Statistic title="端到端 P99 (ms)" value={fmt(report?.e2e_p99_ms)} /></Card></Col>
      </Row>

      <Card title="吞吐量（RPS / TPS）" style={{ marginBottom: 16 }}>
        <div ref={rate.ref} style={{ height: 300 }} />
      </Card>
      <Card title="延迟（ms）">
        <div ref={latency.ref} style={{ height: 300 }} />
      </Card>
    </div>
  );
}
