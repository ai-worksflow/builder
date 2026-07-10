import { I18nProvider } from '@/lib/i18n'
import { WorksflowProvider } from '@/lib/worksflow/store'
import { CollaborationProvider } from '@/lib/collaboration/provider'
import type { CollaborationPlatformClient } from '@/lib/collaboration/platform-adapter'
import { ArtifactWorkspaceProvider } from '@/lib/platform/artifact-provider'
import { PlatformFlowProvider } from '@/lib/platform/flow-provider'
import { AppShell } from './app-shell'

export function WorksflowRoot({
  platformClient,
}: {
  platformClient?: CollaborationPlatformClient
} = {}) {
  return (
    <I18nProvider>
      <WorksflowProvider>
        <CollaborationProvider client={platformClient}>
          <ArtifactWorkspaceProvider>
            <PlatformFlowProvider>
              <AppShell />
            </PlatformFlowProvider>
          </ArtifactWorkspaceProvider>
        </CollaborationProvider>
      </WorksflowProvider>
    </I18nProvider>
  )
}
