import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// В докере статику раздаёт Go-бэкенд. В разработке: 5182 -- vite, 8080 -- go
// (5181 у vite занят doc-index).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5182,
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
});
