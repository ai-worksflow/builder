import { expect, test } from '@playwright/test'

test('Workbench generation flow reaches preview, code, and database states', async ({ page }) => {
  await page.goto('/workbench/planning')

  await expect(page).toHaveURL(/\/workbench\/planning/)
  await expect(page.getByText('Planning...')).toBeVisible()

  const implementPlan = page.getByRole('button', { name: 'Implement this plan' })
  await expect(implementPlan).toBeVisible({ timeout: 7_000 })
  await expect(page).toHaveURL(/\/workbench\/plan-ready/)

  await implementPlan.click()
  await expect(page).toHaveURL(/\/workbench\/building/)
  await expect(page.getByText('Building...', { exact: true })).toBeVisible()

  await expect(page.getByText('Plan completed')).toBeVisible({ timeout: 12_000 })
  await expect(page).toHaveURL(/\/workbench\/complete/)
  await expect(page.getByText('Taskflow', { exact: true })).toBeVisible()
  await expect(page.locator('footer').filter({ hasText: 'Made in Worksflow' })).toBeVisible()

  await page.getByRole('button', { name: 'Code' }).click()
  await expect(page).toHaveURL(/\/workbench\/complete\?view=code/)
  await expect(page.getByText('App.tsx').first()).toBeVisible()
  await expect(page.getByText('[vite] page reload src/components/TaskInput.tsx')).toBeVisible()

  await page.getByRole('button', { name: 'Database' }).click()
  await expect(page).toHaveURL(/\/workbench\/complete\?view=database/)
  await expect(page.getByText('Power up your backend with Worksflow Database')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Ask Worksflow to create a database' })).toBeVisible()
})

test('Blueprint selection can create a Workbench context with linked docs', async ({ page }) => {
  await page.goto('/team/acme/project/tp-crm/blueprint')

  await expect(page).toHaveURL(/\/team\/acme\/project\/tp-crm\/blueprint/)
  await expect(page.getByText('CRM Rewrite Blueprint')).toBeVisible()

  await page.getByRole('button', { name: 'Use in Workbench' }).click()

  await expect(page).toHaveURL(/\/workbench\/planning/)
  await expect(page.getByText('Blueprint context', { exact: true })).toBeVisible()
  await expect(page.locator('textarea')).toHaveValue(/Use Blueprint selection/)
  await expect(page.locator('textarea')).toHaveValue(/Team documents to read:/)
})

test('Import sync and review actions update document state visibly', async ({ page }) => {
  await page.goto('/team/acme/project/tp-crm/imports')

  await expect(page.getByText('Design Import Center')).toBeVisible()
  await page.getByRole('button', { name: 'Sync' }).first().click()
  await expect(page.getByText('Syncing').first()).toBeVisible()
  await expect(page.getByText('Just now').first()).toBeVisible({ timeout: 4_000 })

  await page.goto('/team/acme/project/tp-crm/reviews')
  await expect(page.getByText('Review Center')).toBeVisible()
  await page.getByRole('button', { name: 'Request changes' }).click()
  await expect(page.getByText('Changes Requested').first()).toBeVisible()
  await page.getByRole('button', { name: 'Mark as synced' }).click()
  await expect(page.getByText('Ready for Review').first()).toBeVisible()
})
