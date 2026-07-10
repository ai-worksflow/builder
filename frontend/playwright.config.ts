import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests',
  testMatch: /.*\.spec\.ts/,
  // The integration suite exercises one filesystem-backed collaboration/data runtime.
  // Serialize it so project, session, deployment, and migration assertions stay isolated.
  fullyParallel: false,
  workers: 1,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:3000',
    locale: 'en-US',
    trace: 'on-first-retry',
  },
  webServer: {
    command: 'pnpm dev --hostname 127.0.0.1',
    url: 'http://127.0.0.1:3000',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
