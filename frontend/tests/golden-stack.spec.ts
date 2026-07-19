import { randomUUID } from 'node:crypto'

import { expect, qualificationEnvironment, test } from './qualification-runtime'

const allowHTTP = process.env.WORKSFLOW_GOLDEN_ALLOW_HTTP === 'true'

test.describe('approved Golden Stack reference smoke (not stage-exit evidence)', () => {
  test('QG-REFERENCE-001 checks the currently implemented health/message/browser persistence subset', async ({ page, request }) => {
    const { authorization, goldenOrigin: origin, templateReleaseHash, templateReleaseId } = qualificationEnvironment()
    test.info().annotations.push({
      type: 'qualification-level',
      description: 'partial-smoke-only; no immutable qualification receipt is issued',
    })
    expect(['https:', ...(allowHTTP ? ['http:'] : [])]).toContain(origin.protocol)
    expect(templateReleaseId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i)
    expect(templateReleaseHash).toMatch(/^sha256:[0-9a-f]{64}$/)
    const ready = await request.get(new URL('/api/health/ready', origin).toString(), {
      headers: authorization,
    })
    expect(ready.status()).toBe(200)
    expect(await ready.json()).toEqual({
      schemaVersion: 'golden-health/v1',
      status: 'ready',
      services: { web: 'ready', api: 'ready', database: 'ready' },
      templateRelease: { id: templateReleaseId, contentHash: templateReleaseHash },
    })

    const tenantA = randomUUID()
    const tenantB = randomUUID()
    const recordId = randomUUID()
    const summary = `Golden persisted message ${recordId}`
    const create = await request.post(new URL('/api/e2e/messages', origin).toString(), {
      headers: { ...authorization, 'X-Tenant-ID': tenantA, 'Idempotency-Key': `golden-${recordId}` },
      data: { id: recordId, summary },
    })
    expect(create.status()).toBe(201)
    expect(await create.json()).toEqual({
      schemaVersion: 'golden-message/v1', id: recordId, tenantId: tenantA, summary,
    })

    const replay = await request.post(new URL('/api/e2e/messages', origin).toString(), {
      headers: { ...authorization, 'X-Tenant-ID': tenantA, 'Idempotency-Key': `golden-${recordId}` },
      data: { id: recordId, summary },
    })
    expect(replay.status()).toBe(200)
    expect(replay.headers()['x-idempotent-replay']).toBe('true')

    const persisted = await request.get(new URL(`/api/e2e/messages/${recordId}`, origin).toString(), {
      headers: { ...authorization, 'X-Tenant-ID': tenantA },
    })
    expect(persisted.status()).toBe(200)
    expect(await persisted.json()).toEqual({
      schemaVersion: 'golden-message/v1', id: recordId, tenantId: tenantA, summary,
    })
    const crossTenant = await request.get(new URL(`/api/e2e/messages/${recordId}`, origin).toString(), {
      headers: { ...authorization, 'X-Tenant-ID': tenantB },
    })
    expect(crossTenant.status()).toBe(404)

    await page.setExtraHTTPHeaders({ ...authorization, 'X-Tenant-ID': tenantA })
    await page.goto(new URL('/e2e', origin).toString())
    await expect(page.getByRole('heading', { name: 'Golden Stack' })).toBeVisible()
    await page.getByLabel('Message').fill(summary)
    await page.getByRole('button', { name: 'Save message' }).click()
    await expect(page.getByText(summary, { exact: true })).toBeVisible()
    await page.reload()
    await expect(page.getByText(summary, { exact: true })).toBeVisible()
  })
})
