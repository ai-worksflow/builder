'use client'

import { useEffect, useState } from 'react'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { useLocalizedLabels } from './use-localized-labels'
import { LanguageToggle } from './language-toggle'
import { CollaborationCenter } from './collaboration-center'
import {
  Bell,
  Database,
  GitBranch,
  KeyRound,
  Link2,
  Lock,
  Plug,
  Rocket,
  Settings,
  ShieldCheck,
  Users2,
} from 'lucide-react'

export function SettingsCenter() {
  const {
    projectName,
    setProjectName,
    setSurface,
    setView,
    setTeamView,
    linkedDocIds,
    documents,
    productProject,
    preferences,
    updateUserPreferences,
  } = useWorksflow()
  const { t } = useI18n()
  const labels = useLocalizedLabels()
  const [draftName, setDraftName] = useState(projectName)
  useEffect(() => setDraftName(projectName), [projectName])
  const linkedDocs = linkedDocIds
    .map((id) => documents.find((doc) => doc.id === id))
    .filter(Boolean)

  return (
    <div className="flex h-full flex-col bg-background">
      <header className="flex min-h-[50px] shrink-0 items-center justify-between gap-3 border-b border-border bg-panel px-5 py-2 max-sm:px-4">
        <div className="flex items-center gap-2">
          <Settings className="h-4 w-4 text-primary-bright" />
          <span className="text-sm font-semibold text-foreground">{t('settings.title')}</span>
        </div>
        <div className="flex items-center gap-2">
          <LanguageToggle />
          <button
            type="button"
            onClick={() => setSurface('workbench')}
            className="rounded-md border border-border px-3 py-1.5 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
          >
            {t('settings.backToWorkbench')}
          </button>
        </div>
      </header>

      <main className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
        <div className="mx-auto grid max-w-6xl grid-cols-1 gap-4 px-6 py-6 lg:grid-cols-[1fr_360px] max-sm:px-4">
          <section className="space-y-4">
            <div className="rounded-lg border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-foreground">
                <ShieldCheck className="h-4 w-4 text-primary-bright" />
                {t('settings.projectSettings')}
              </div>
              <label className="block text-[12px] text-muted-foreground">
                {t('settings.projectName')}
                <div className="mt-1.5 flex gap-2 max-sm:flex-wrap">
                  <input
                    value={draftName}
                    onChange={(event) => setDraftName(event.target.value)}
                    className="min-w-[180px] flex-1 rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground outline-none focus:border-primary/60 focus:ring-1 focus:ring-primary/40"
                  />
                  <button
                    type="button"
                    onClick={() => setProjectName(draftName.trim() || projectName)}
                    className="rounded-md bg-primary px-3 py-2 text-[12px] font-semibold text-primary-foreground hover:bg-primary-bright"
                  >
                    {t('common.save')}
                  </button>
                </div>
              </label>
            </div>

            <CollaborationCenter />

            <div className="rounded-lg border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-foreground">
                <Plug className="h-4 w-4 text-primary-bright" />
                {t('settings.integrations')}
              </div>
              <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                <IntegrationCard
                  icon={GitBranch}
                  title={t('settings.github')}
                  description={
                    productProject.githubSettings.status === 'connected'
                      ? t('settings.githubConnected')
                      : t('settings.githubDisconnected')
                  }
                  action={
                    productProject.githubSettings.status === 'connected'
                      ? t('common.connected')
                      : t('settings.openWorkbench')
                  }
                  onClick={() => setSurface('workbench')}
                />
                <IntegrationCard
                  icon={Database}
                  title={t('settings.database')}
                  description={t('settings.databaseDescription')}
                  action={t('settings.openDatabase')}
                  onClick={() => {
                    setSurface('workbench')
                    setView('database')
                  }}
                />
                <IntegrationCard
                  icon={Users2}
                  title={t('settings.membersPermissions')}
                  description={t('settings.membersDescription')}
                  action={t('settings.openMembers')}
                  onClick={() => {
                    setSurface('team')
                    setTeamView('members')
                  }}
                />
                <IntegrationCard
                  icon={Rocket}
                  title={t('settings.publish')}
                  description={t('settings.publishDescription')}
                  action={t('settings.openWorkbench')}
                  onClick={() => setSurface('workbench')}
                />
              </div>
            </div>
          </section>

          <aside className="space-y-4">
            <div className="rounded-lg border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-foreground">
                <Link2 className="h-4 w-4 text-primary-bright" />
                {t('settings.workbenchContext')}
              </div>
              <div className="space-y-2">
                {linkedDocs.map((doc) => (
                  <div
                    key={doc!.id}
                    className="rounded-md border border-border bg-card px-3 py-2 text-[12px]"
                  >
                    <div className="font-medium text-foreground">{doc!.title}</div>
                    <div className="mt-0.5 text-faint-foreground">
                      {labels.docStatus(doc!.status)}
                    </div>
                  </div>
                ))}
              </div>
            </div>

            <div className="rounded-lg border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-foreground">
                <Bell className="h-4 w-4 text-primary-bright" />
                {t('settings.notificationRules')}
              </div>
              <PreferenceToggle
                label={t('settings.notifyBlocking')}
                checked={preferences.notifyBlockingChanges}
                onChange={(checked) => updateUserPreferences({ notifyBlockingChanges: checked })}
              />
              <PreferenceToggle
                label={t('settings.notifyReviewers')}
                checked={preferences.notifyReviewSync}
                onChange={(checked) => updateUserPreferences({ notifyReviewSync: checked })}
              />
              <PreferenceToggle
                label={t('settings.requireApproval')}
                checked={preferences.requireApprovedContext}
                onChange={(checked) => updateUserPreferences({ requireApprovedContext: checked })}
              />
            </div>

            <div className="rounded-lg border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-foreground">
                <Lock className="h-4 w-4 text-primary-bright" />
                {t('settings.access')}
              </div>
              <div className="flex items-center justify-between rounded-md border border-border bg-card px-3 py-2 text-[12px] text-muted-foreground">
                {t('settings.defaultRole')}
                <label className="inline-flex items-center gap-1 text-foreground">
                  <KeyRound className="h-3.5 w-3.5 text-primary-bright" />
                  <select
                    value={preferences.defaultProjectRole}
                    onChange={(event) => {
                      const defaultProjectRole = event.target.value as typeof preferences.defaultProjectRole
                      updateUserPreferences({ defaultProjectRole })
                    }}
                    className="bg-transparent text-[12px] text-foreground outline-none"
                    aria-label={t('settings.defaultRole')}
                  >
                    <option value="viewer">{t('common.viewer')}</option>
                    <option value="commenter">{t('common.commenter')}</option>
                    <option value="editor">{t('common.editor')}</option>
                  </select>
                </label>
              </div>
            </div>
          </aside>
        </div>
      </main>
    </div>
  )
}

function PreferenceToggle({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <label className="flex items-center justify-between rounded-md px-1 py-2 text-[12px] text-muted-foreground">
      {label}
      <input
        type="checkbox"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
      />
    </label>
  )
}

function IntegrationCard({
  icon: Icon,
  title,
  description,
  action,
  onClick,
}: {
  icon: typeof GitBranch
  title: string
  description: string
  action: string
  onClick: () => void
}) {
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="flex items-center gap-2">
        <span className="flex h-8 w-8 items-center justify-center rounded-md bg-white/5">
          <Icon className="h-4 w-4 text-primary-bright" />
        </span>
        <div className="text-sm font-medium text-foreground">{title}</div>
      </div>
      <p className="mt-2 min-h-10 text-[12px] leading-relaxed text-muted-foreground">
        {description}
      </p>
      <button
        type="button"
        onClick={onClick}
        className="mt-3 rounded-md border border-border px-2.5 py-1.5 text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground"
      >
        {action}
      </button>
    </div>
  )
}
