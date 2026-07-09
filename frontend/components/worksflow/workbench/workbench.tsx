'use client'

import { useWorksflow } from '@/lib/worksflow/store'
import { TopBar } from './top-bar'
import { ChatPanel } from './chat-panel'
import { PreviewPanel } from './preview-panel'
import { CodePanel } from './code-panel'
import { DatabasePanel } from './database-panel'

export function Workbench() {
  const { view } = useWorksflow()

  return (
    <div className="flex h-full flex-col">
      <TopBar />
      <div className="flex min-h-0 flex-1 max-lg:flex-col max-lg:overflow-y-auto max-lg:scrollbar-thin">
        <ChatPanel />
        <main className="min-h-0 min-w-0 flex-1 p-3 max-lg:min-h-[420px] max-lg:flex-none max-lg:p-2">
          <div className="flex h-full flex-col overflow-hidden rounded-lg border border-border bg-panel">
            {view === 'preview' && <PreviewPanel />}
            {view === 'code' && <CodePanel />}
            {view === 'database' && <DatabasePanel />}
          </div>
        </main>
      </div>
    </div>
  )
}
