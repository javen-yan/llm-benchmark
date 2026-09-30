import React from 'react';
import { HashRouter, Link, Route, Routes, useLocation } from 'react-router-dom';
import { Layout, Menu } from 'antd';
import { BarChartOutlined, DashboardOutlined, PlusCircleOutlined } from '@ant-design/icons';
import RunList from './pages/RunList';
import NewRun from './pages/NewRun';
import RunDetail from './pages/RunDetail';
import Compare from './pages/Compare';

const { Header, Content, Footer } = Layout;

function Nav() {
  const loc = useLocation();
  const key = loc.pathname === '/new' ? '/new'
    : loc.pathname === '/compare' ? '/compare'
    : loc.pathname.startsWith('/runs/') ? '/' : loc.pathname;
  return (
    <Menu
      theme="dark"
      mode="horizontal"
      selectedKeys={[key]}
      items={[
        { key: '/', icon: <DashboardOutlined />, label: <Link to="/">运行列表</Link> },
        { key: '/new', icon: <PlusCircleOutlined />, label: <Link to="/new">新建测试</Link> },
        { key: '/compare', icon: <BarChartOutlined />, label: <Link to="/compare">对比</Link> },
      ]}
    />
  );
}

export default function App() {
  return (
    <HashRouter>
      <Layout style={{ minHeight: '100vh' }}>
        <Header style={{ display: 'flex', alignItems: 'center', gap: 24, padding: '0 24px' }}>
          <div style={{ color: '#fff', fontSize: 18, fontWeight: 600, whiteSpace: 'nowrap' }}>
            LLM Benchmark
          </div>
          <Nav />
        </Header>
        <Content style={{ padding: '24px', maxWidth: 1280, margin: '0 auto', width: '100%' }}>
          <Routes>
            <Route path="/" element={<RunList />} />
            <Route path="/new" element={<NewRun />} />
            <Route path="/runs/:id" element={<RunDetail />} />
            <Route path="/compare" element={<Compare />} />
          </Routes>
        </Content>
        <Footer style={{ textAlign: 'center', color: '#666' }}>
          LLM Benchmark Platform · Go + React
        </Footer>
      </Layout>
    </HashRouter>
  );
}
