import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// В докере статику раздаёт Go-бэкенд. В разработке vite на 5178 проксирует
// API и опубликованные планы на бэкенд (8790).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5178,
    proxy: {
      '/api': 'http://localhost:8790',
      '/plans': 'http://localhost:8790',
    },
  },
});
