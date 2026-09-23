import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// В докере статику раздаёт сам Go-бэкенд, и прокси не нужен. В режиме разработки
// фронт и бэкенд разнесены по портам: 5176 -- vite, 8080 -- go.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5176,
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
});
