import React, { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Button, Card, Col, Form, Input, InputNumber, Radio, Row,
  Select, Switch, Typography, message,
} from 'antd';
import { createRun, listEngines, SpecV2 } from '../api';

const { Title } = Typography;

const loadModes = [
  { value: 'closed_loop', label: '闭环（固定并发）' },
  { value: 'open_loop', label: '开环（固定速率）' },
  { value: 'poisson', label: '泊松到达（开环）' },
];

// Go time.ParseDuration 允许的单位
const durationRe = /^\d+(\.\d+)?(ns|us|µs|ms|s|m|h)$/;

export default function NewRun() {
  const [form] = Form.useForm();
  const [engines, setEngines] = useState<string[]>(['mock', 'guidellm']);
  const [submitting, setSubmitting] = useState(false);
  const navigate = useNavigate();

  const mode = Form.useWatch('mode', form);
  const loadBy = Form.useWatch('loadBy', form);
  const needRate = mode === 'open_loop' || mode === 'poisson';

  useEffect(() => {
    listEngines()
      .then((list) => {
        if (list.length > 0) setEngines(list.map((e) => e.name));
      })
      .catch(() => {});
  }, []);

  const onFinish = async (v: any) => {
    const spec: SpecV2 = {
      name: v.name,
      target: {
        endpoint: v.endpoint,
        protocol: v.protocol,
        model: v.model,
        ...(v.api_key ? { api_key: v.api_key } : {}),
      },
      engine: { type: v.engine },
      dataset: {
        type: 'random',
        input_tokens: v.input_tokens,
        output_tokens: v.output_tokens,
        ...(v.seed !== undefined && v.seed !== null ? { seed: v.seed } : {}),
      },
      load: {
        mode: v.mode,
        concurrency: v.concurrency,
        ...(v.loadBy === 'requests' ? { requests: v.requests } : { duration: v.duration }),
        ...(needRate ? { rate: v.rate } : {}),
        stream: v.stream,
      },
      ...(v.warmup_duration ? { warmup: { duration: v.warmup_duration } } : {}),
    };
    const th: NonNullable<SpecV2['thresholds']> = {};
    if (v.ttft_p95) th.ttft_p95 = v.ttft_p95;
    if (v.ttft_p99) th.ttft_p99 = v.ttft_p99;
    if (v.max_ttft_ms) th.max_ttft_ms = v.max_ttft_ms;
    if (v.min_tps) th.min_tps = v.min_tps;
    if (v.error_rate !== undefined && v.error_rate !== null) th.error_rate = v.error_rate;
    if (v.p99_latency_ms) th.p99_latency_ms = v.p99_latency_ms;
    if (Object.keys(th).length > 0) spec.thresholds = th;

    setSubmitting(true);
    try {
      const id = await createRun(spec);
      message.success('已提交');
      navigate(`/runs/${id}`);
    } catch (e: any) {
      message.error('提交失败：' + (e?.response?.data?.error ?? e?.message ?? e));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div>
      <Title level={3}>新建 Benchmark</Title>
      <Form
        form={form}
        layout="vertical"
        onFinish={onFinish}
        initialValues={{
          engine: 'mock', protocol: 'openai', mode: 'closed_loop', loadBy: 'requests',
          concurrency: 4, requests: 40, input_tokens: 128, output_tokens: 32,
          stream: true, endpoint: 'http://127.0.0.1:9000', model: 'mock-llm',
        }}
      >
        <Card title="基本信息" style={{ marginBottom: 16 }}>
          <Row gutter={16}>
            <Col span={12}>
              <Form.Item name="name" label="测试名称" rules={[{ required: true, message: '请输入名称' }]}>
                <Input placeholder="例如：qwen3-8b-smoke" />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="engine" label="引擎" rules={[{ required: true }]}>
                <Select options={engines.map((e) => ({ value: e, label: e }))} />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={16}>
            <Col span={12}>
              <Form.Item name="endpoint" label="目标 Endpoint" rules={[{ required: true, message: '请输入目标地址' }]}>
                <Input placeholder="http://127.0.0.1:9000" />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="protocol" label="协议" rules={[{ required: true }]}>
                <Select options={[{ value: 'openai', label: 'openai' }]} />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="model" label="模型名" rules={[{ required: true, message: '请输入模型名' }]}>
                <Input placeholder="mock-llm" />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item name="api_key" label="API Key（可选）">
            <Input.Password placeholder="留空则不带鉴权头" />
          </Form.Item>
        </Card>

        <Card title="数据集" style={{ marginBottom: 16 }}>
          <Row gutter={16}>
            <Col span={6}>
              <Form.Item label="数据集类型">
                <Input value="random" disabled />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="input_tokens" label="输入 token 数" rules={[{ required: true }]}>
                <InputNumber min={1} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="output_tokens" label="输出 token 数" rules={[{ required: true }]}>
                <InputNumber min={1} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="seed" label="随机种子（可选）">
                <InputNumber style={{ width: '100%' }} placeholder="默认随机" />
              </Form.Item>
            </Col>
          </Row>
        </Card>

        <Card title="负载策略" style={{ marginBottom: 16 }}>
          <Row gutter={16}>
            <Col span={8}>
              <Form.Item name="mode" label="负载模式" rules={[{ required: true }]}>
                <Select options={loadModes} />
              </Form.Item>
            </Col>
            <Col span={8}>
              <Form.Item name="concurrency" label={needRate ? '最大并发数' : '并发数'} rules={[{ required: true }]}>
                <InputNumber min={1} max={4096} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col span={8}>
              <Form.Item name="stream" label="流式输出（SSE）" valuePropName="checked">
                <Switch />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={16}>
            <Col span={8}>
              <Form.Item label="终止条件">
                <Form.Item name="loadBy" noStyle>
                  <Radio.Group options={[
                    { value: 'requests', label: '按请求数' },
                    { value: 'duration', label: '按时长' },
                  ]} />
                </Form.Item>
              </Form.Item>
            </Col>
            {loadBy === 'requests' ? (
              <Col span={8}>
                <Form.Item name="requests" label="总请求数" rules={[{ required: true, message: '请输入总请求数' }]}>
                  <InputNumber min={1} style={{ width: '100%' }} />
                </Form.Item>
              </Col>
            ) : (
              <Col span={8}>
                <Form.Item
                  name="duration" label="持续时长"
                  rules={[
                    { required: true, message: '请输入持续时长' },
                    { pattern: durationRe, message: '格式如 300s、5m、1h' },
                  ]}
                >
                  <Input placeholder="例如：300s" />
                </Form.Item>
              </Col>
            )}
            {needRate && (
              <Col span={8}>
                <Form.Item name="rate" label="目标速率（req/s）" rules={[{ required: true, message: '开环模式必须填写速率' }]}>
                  <InputNumber min={0.1} step={0.1} style={{ width: '100%' }} />
                </Form.Item>
              </Col>
            )}
          </Row>
          <Row gutter={16}>
            <Col span={8}>
              <Form.Item
                name="warmup_duration" label="预热时长（可选）"
                rules={[{ pattern: durationRe, message: '格式如 30s、1m' }]}
              >
                <Input placeholder="例如：30s，留空不预热" />
              </Form.Item>
            </Col>
          </Row>
        </Card>

        <Card title="SLO 阈值（可选）" style={{ marginBottom: 16 }}>
          <Row gutter={16}>
            <Col span={8}>
              <Form.Item name="ttft_p95" label="TTFT P95 上限 (ms)">
                <InputNumber min={1} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
            <Col span={8}>
              <Form.Item name="ttft_p99" label="TTFT P99 上限 (ms)">
                <InputNumber min={1} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
            <Col span={8}>
              <Form.Item name="max_ttft_ms" label="TTFT 最大值上限 (ms)">
                <InputNumber min={1} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={16}>
            <Col span={8}>
              <Form.Item name="min_tps" label="最小 TPS">
                <InputNumber min={0.1} step={0.1} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
            <Col span={8}>
              <Form.Item name="error_rate" label="错误率上限 (0~1)">
                <InputNumber min={0} max={1} step={0.01} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
            <Col span={8}>
              <Form.Item name="p99_latency_ms" label="P99 延迟上限 (ms)">
                <InputNumber min={1} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
          </Row>
        </Card>

        <Form.Item>
          <Button type="primary" htmlType="submit" loading={submitting} size="large">提交运行</Button>
        </Form.Item>
      </Form>
    </div>
  );
}
