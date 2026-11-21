import { defineConfig } from 'vitest/config';
export default defineConfig({
  esbuild: { jsx: 'automatic' },
  test: { environment: 'jsdom', include: ['**/*.test.jsx'], globals: true, reporters: 'basic' },
});
