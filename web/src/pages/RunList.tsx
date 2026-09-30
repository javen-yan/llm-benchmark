import React, { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button, Space, Table, Tag, Typography, message, Popconfirm } from 'antd';
import { PlusOutlined, ReloadOutlined, BarChartOutlined } from '@ant-design/icons';
import {
  cancelRun, formatTime, isActive, listRuns,
  statusColor, statusText, Run, RunStatus,
} from '../api';

const { Title } = Typography;

export default function RunList() {
  const [runs, setRuns] = useState<Run[]>([]);
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  const navigate = useNavigate();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setRuns(await listRuns());
    } catch (e: any) {
      message.error('加载运行列表失败：' + (e?.message ?? e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [load]);

  const onCancel = async (id: string) => {
    try {
      await cancelRun(id);
      message.success('已取消');
      load();
    } catch (e: any) {
      message.error('取消失败：' + (e?.response?.data?.error ?? e?.message ?? e));
    }
  };

  const onCompare = () => {
    if (selected.length < 2) {
      message.warning('请至少选择 2 个运行进行对比');
      return;
    }
    navigate(`/compare?ids=${selected.join(',')}`);
  };

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Title level={3} style={{ margin: 0 }}>运行列表</Title>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>刷新</Button>
          <Button
            icon={<BarChartOutlined />}
            disabled={selected.length < 2}
            onClick={onCompare}
          >
            对比选中 ({selected.length})
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/new')}>新建测试</Button>
        </Space>
      </div>
      <Table<Run>
        rowKey="id"
        dataSource={runs}
        loading={loading}
        rowSelection={{
          selectedRowKeys: selected,
          onChange: (keys) => setSelected(keys as string[]),
        }}
        onRow={(r) => ({ onClick: () => navigate(`/runs/${r.id}`), style: { cursor: 'pointer' } })}
        columns={[
          { title: '名称', dataIndex: 'name', key: 'name' },
          { title: '引擎', dataIndex: 'engine', key: 'engine', width: 110 },
          {
            title: '状态', dataIndex: 'status', key: 'status', width: 120,
            render: (s: RunStatus) => <Tag color={statusColor[s] ?? 'default'}>{statusText[s] ?? s}</Tag>,
          },
          {
            title: '创建时间', dataIndex: 'created_at', key: 'created_at', width: 180,
            render: (v: string) => formatTime(v),
          },
          {
            title: '操作', key: 'action', width: 140,
            render: (_, r) => (
              <Space onClick={(e) => e.stopPropagation()}>
                <Button size="small" onClick={() => navigate(`/runs/${r.id}`)}>详情</Button>
                {isActive(r.status) && (
                  <Popconfirm title="确定取消这次运行？" onConfirm={() => onCancel(r.id)} okText="确定" cancelText="取消">
                    <Button size="small" danger>取消</Button>
                  </Popconfirm>
                )}
              </Space>
            ),
          },
        ]}
        pagination={{ pageSize: 20, showSizeChanger: false }}
      />
    </div>
  );
}
