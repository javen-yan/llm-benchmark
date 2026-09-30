import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Alert, Button, Card, Empty, Space, Spin, Table, Tag, Typography, message } from 'antd';
import { ArrowLeftOutlined, CheckCircleOutlined, CloseCircleOutlined } from '@ant-design/icons';
import {
  compareRuns, fmt, statusColor, statusText, CompareRun, FairnessItem,
} from '../api';

const { Title, Text } = Typography;

const fairnessLabels: Record<string, string> = {
  model: '模型',
  engine: '引擎',
  dataset_type: '数据集',
  input_tokens: '输入 token',
  output_tokens: '输出 token',
  seed: '随机种子',
  'hardware.cpu': 'CPU 核数',
};

// 对比表里的指标展示顺序
const metricOrder: Array<{ id: string; aggs: string[] }> = [
  { id: 'llm.ttft', aggs: ['p50', 'p99'] },
  { id: 'llm.tpot', aggs: ['p50', 'p99'] },
  { id: 'llm.itl', aggs: ['p50', 'p99'] },
  { id: 'llm.e2e', aggs: ['p50', 'p99'] },
  { id: 'llm.queue_time', aggs: ['p50', 'p99'] },
  { id: 'llm.request_rate', aggs: ['value'] },
  { id: 'llm.input_tps', aggs: ['value'] },
  { id: 'llm.output_tps', aggs: ['value'] },
  { id: 'llm.total_tps', aggs: ['value'] },
  { id: 'llm.error_rate', aggs: ['value'] },
  { id: 'llm.request_count', aggs: ['count'] },
  { id: 'llm.success_count', aggs: ['count'] },
];

interface CompareRow {
  key: string;
  metric_id: string;
  aggregation: string;
  vals: Array<number | undefined>;
}

export default function Compare() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const ids = useMemo(
    () => (params.get('ids') ?? '').split(',').map((s) => s.trim()).filter(Boolean),
    [params],
  );
  const [runs, setRuns] = useState<CompareRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState('');

  const load = useCallback(async () => {
    if (ids.length < 2) {
      setErr('请至少选择 2 个运行进行对比');
      setLoading(false);
      return;
    }
    setLoading(true);
    setErr('');
    try {
      setRuns(await compareRuns(ids));
    } catch (e: any) {
      setErr('加载对比数据失败：' + (e?.response?.data?.error ?? e?.message ?? e));
    } finally {
      setLoading(false);
    }
  }, [ids]);

  useEffect(() => {
    load();
  }, [load]);

  const metricRows: CompareRow[] = useMemo(() => {
    const rows: CompareRow[] = [];
    for (const { id, aggs } of metricOrder) {
      for (const agg of aggs) {
        if (!runs.some((r) => r.metrics?.[id]?.[agg] !== undefined)) continue;
        rows.push({
          key: `${id}:${agg}`,
          metric_id: id,
          aggregation: agg,
          vals: runs.map((r) => r.metrics?.[id]?.[agg]),
        });
      }
    }
    return rows;
  }, [runs]);

  if (loading) {
    return <Spin size="large" style={{ display: 'block', margin: '80px auto' }} />;
  }

  const fairness = runs[0]?.fairness ?? [];

  return (
    <div>
      <Space style={{ marginBottom: 16 }} wrap>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/')}>返回列表</Button>
        <Title level={3} style={{ margin: 0 }}>运行对比</Title>
        {runs.map((r) => (
          <Tag key={r.id} color={statusColor[r.status] ?? 'default'}>
            {r.name}（{statusText[r.status] ?? r.status}）
          </Tag>
        ))}
      </Space>

      {err && <Alert type="error" message={err} style={{ marginBottom: 16 }} />}

      <Card title="公平性校验（快照字段是否一致）" style={{ marginBottom: 16 }}>
        {fairness.length === 0 ? (
          <Empty description="暂无公平性数据" />
        ) : (
          <Table<FairnessItem>
            rowKey="field"
            dataSource={fairness}
            pagination={false}
            size="small"
            columns={[
              { title: '字段', dataIndex: 'field', key: 'field', width: 140,
                render: (f: string) => fairnessLabels[f] ?? f },
              ...runs.map((r, i) => ({
                title: r.name, key: r.id, width: 180,
                render: (_: unknown, row: FairnessItem) => (
                  <Text>{String(row.values[i] ?? '-')}</Text>
                ),
              })),
              {
                title: '是否一致', key: 'match', width: 120,
                render: (_: unknown, row: FairnessItem) => row.match ? (
                  <Tag icon={<CheckCircleOutlined />} color="success">一致</Tag>
                ) : (
                  <Tag icon={<CloseCircleOutlined />} color="error">不一致</Tag>
                ),
              },
            ]}
          />
        )}
      </Card>

      <Card title="核心指标对照">
        {metricRows.length === 0 ? (
          <Empty description="暂无指标数据" />
        ) : (
          <Table<CompareRow>
            rowKey="key"
            dataSource={metricRows}
            pagination={false}
            size="small"
            scroll={{ x: true }}
            columns={[
              { title: '指标', dataIndex: 'metric_id', key: 'metric_id', width: 160, fixed: 'left' },
              { title: '聚合', dataIndex: 'aggregation', key: 'aggregation', width: 90, fixed: 'left',
                render: (a: string) => a.toUpperCase() },
              ...runs.map((r) => ({
                title: r.name, key: r.id, width: 160,
                render: (_: unknown, row: CompareRow) => {
                  const idx = runs.indexOf(r);
                  return fmt(row.vals[idx]);
                },
              })),
            ]}
          />
        )}
      </Card>
    </div>
  );
}
