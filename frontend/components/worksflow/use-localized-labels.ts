'use client'

import { useCallback } from 'react'
import { useI18n } from '@/lib/i18n'
import {
  BLUEPRINT_EDGE_LABEL_KEY,
  BLUEPRINT_NODE_LABEL_KEY,
  DEP_TYPE_LABEL_KEY,
  DOC_STATUS_LABEL_KEY,
  DOC_TYPE_LABEL_KEY,
  IMPORT_SOURCE_LABEL_KEY,
  ROLE_LABEL_KEY,
  SYNC_STATUS_LABEL_KEY,
} from '@/lib/worksflow/labels'
import type {
  BlueprintEdgeType,
  BlueprintNodeType,
  DependencyType,
  DocMemberRole,
  DocStatus,
  DocType,
  ImportSource,
  SyncStatus,
} from '@/lib/worksflow/types'

export function useLocalizedLabels() {
  const { t } = useI18n()

  const docType = useCallback((value: DocType) => t(DOC_TYPE_LABEL_KEY[value]), [t])
  const docStatus = useCallback((value: DocStatus) => t(DOC_STATUS_LABEL_KEY[value]), [t])
  const role = useCallback((value: DocMemberRole) => t(ROLE_LABEL_KEY[value]), [t])
  const dependency = useCallback((value: DependencyType) => t(DEP_TYPE_LABEL_KEY[value]), [t])
  const blueprintNode = useCallback(
    (value: BlueprintNodeType) => t(BLUEPRINT_NODE_LABEL_KEY[value]),
    [t],
  )
  const blueprintEdge = useCallback(
    (value: BlueprintEdgeType) => t(BLUEPRINT_EDGE_LABEL_KEY[value]),
    [t],
  )
  const importSource = useCallback(
    (value: ImportSource) => t(IMPORT_SOURCE_LABEL_KEY[value]),
    [t],
  )
  const syncStatus = useCallback(
    (value: SyncStatus) => t(SYNC_STATUS_LABEL_KEY[value]),
    [t],
  )

  return {
    blueprintEdge,
    blueprintNode,
    dependency,
    docStatus,
    docType,
    importSource,
    role,
    syncStatus,
  }
}
