import { createWorkspace } from './workspace-model'

export function createInitialWorkspace(projectName = 'Untitled project') {
  return createWorkspace({
    id: 'workspace-p1',
    name: projectName,
    files: [],
  })
}
