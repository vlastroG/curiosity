import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// В докере статику раздаёт сам Go-бэкенд, и прокси не нужен. В режиме разработки
// фронт и бэкенд разнесены по портам: 5174 -- vite, 8080 -- go. Порт 5173 занят
// контейнером, поэтому dev-сервер намеренно сдвинут.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5174,
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
});
