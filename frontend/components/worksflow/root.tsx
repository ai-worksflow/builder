import { I18nProvider } from '@/lib/i18n'
import { WorksflowProvider } from '@/lib/worksflow/store'
import { AppShell } from './app-shell'

export function WorksflowRoot() {
  return (
    <I18nProvider>
      <WorksflowProvider>
        <AppShell />
      </WorksflowProvider>
    </I18nProvider>
  )
}
