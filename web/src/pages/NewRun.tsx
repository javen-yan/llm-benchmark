import React, { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button, Card, Col, Form, Input, InputNumber, Row, Select, Switch, Typography, message } from 'antd';
import { createRun, listEngines, Spec } from '../api';

const { Title } = Typography;

export default function NewRun() {
  const [form] = Form.useForm();
  const [engines, setEngines] = useState<string[]>(['mock', 'guidellm']);
  const [submitting, setSubmitting] = useState(false);
  const navigate = useNavigate();

  useEffect(() => {
    listEngines().then(setEngines).catch(() => {});
  }, []);

  const onFinish = async (v: any) => {
    const spec: Spec = {
      name: v.name,
      target: { base_url: v.base_url, model: v.model, ...(v.api_key ? { api_key: v.api_key } : {}) },
      engine: v.engine,
      workload: {
        concurrency: v.concurrency,
        requests: v.requests,
        input_tokens: v.input_tokens,
        output_tokens: v.output_tokens,
        stream: v.stream,
      },
    };
    const slo: Spec['slo'] = {};
    if (v.p99_latency_ms) slo.p99_latency_ms = v.p99_latency_ms;
    if (v.min_tps) slo.min_tps = v.min_tps;
    if (v.max_ttft_ms) slo.max_ttft_ms = v.max_ttft_ms;
    if (Object.keys(slo).length > 0) spec.slo = slo;

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
      <Card>
        <Form
          form={form}
          layout="vertical"
          onFinish={onFinish}
          initialValues={{
            engine: 'mock', concurrency: 4, requests: 40,
            input_tokens: 128, output_tokens: 32, stream: true,
            base_url: 'http://127.0.0.1:9000', model: 'mock-llm',
          }}
        >
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
              <Form.Item name="base_url" label="目标 Base URL" rules={[{ required: true, message: '请输入目标地址' }]}>
                <Input placeholder="http://127.0.0.1:9000" />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="model" label="模型名" rules={[{ required: true, message: '请输入模型名' }]}>
                <Input placeholder="mock-llm" />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item name="api_key" label="API Key（可选）">
            <Input.Password placeholder="留空则不带鉴权头" />
          </Form.Item>
          <Row gutter={16}>
            <Col span={6}>
              <Form.Item name="concurrency" label="并发数" rules={[{ required: true }]}>
                <InputNumber min={1} max={512} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="requests" label="总请求数" rules={[{ required: true }]}>
                <InputNumber min={1} style={{ width: '100%' }} />
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
          </Row>
          <Row gutter={16}>
            <Col span={6}>
              <Form.Item name="stream" label="流式输出" valuePropName="checked">
                <Switch />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="p99_latency_ms" label="SLO：P99 延迟上限 (ms)">
                <InputNumber min={1} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="min_tps" label="SLO：最小 TPS">
                <InputNumber min={1} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
            <Col span={6}>
              <Form.Item name="max_ttft_ms" label="SLO：TTFT 上限 (ms)">
                <InputNumber min={1} style={{ width: '100%' }} placeholder="可选" />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={submitting}>提交运行</Button>
          </Form.Item>
        </Form>
      </Card>
    </div>
  );
}
