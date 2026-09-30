import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// 构建产物直接输出到 Go 的 go:embed 目录，base 用相对路径。
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
});
