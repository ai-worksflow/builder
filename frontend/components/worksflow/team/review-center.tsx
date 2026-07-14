'use client'

import { useMemo, useState, type FormEvent } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useWorksflow } from '@/lib/worksflow/store'
import { reviewCandidatesForGovernance } from '@/lib/worksflow/project-governance'
import { cn } from '@/lib/utils'
import { Check, CircleAlert, Loader2, MessageSquare, RefreshCw, X } from 'lucide-react'

type ReviewFilter = 'all' | 'pending' | 'approved' | 'changesRequested'

export function ReviewCenter() {
  const {
    loading,
    session,
    project,
    members,
    reviews,
    reviewTargets,
    error,
    can,
    refresh,
    requestReview,
    decideReview,
  } = useCollaboration()
  const { setSurface } = useWorksflow()
  const [filter, setFilter] = useState<ReviewFilter>('all')
  const [targetRevisionId, setTargetRevisionId] = useState('')
  const [reviewerId, setReviewerId] = useState('')
  const [summary, setSummary] = useState('')
  const [soloReviewConfirmations, setSoloReviewConfirmations] = useState<ReadonlySet<string>>(
    () => new Set(),
  )
  const [approvalSummaries, setApprovalSummaries] = useState<Readonly<Record<string, string>>>({})
  const isSoloProject = project?.governanceMode === 'solo'
  const reviewerCandidates = reviewCandidatesForGovernance(
    members,
    session.signedIn ? session.user.id : null,
    project?.governanceMode ?? 'team',
  )
  const effectiveReviewerId = isSoloProject ? reviewerCandidates[0]?.user.id ?? '' : reviewerId
  const target = reviewTargets.find((item) => item.revisionId === targetRevisionId) ?? reviewTargets[0]
  const visibleReviews = useMemo(
    () => reviews.filter((review) => filter === 'all' || review.state === filter),
    [filter, reviews],
  )

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!target || !effectiveReviewerId || !summary.trim()) return
    if (await requestReview(summary.trim(), target, [effectiveReviewerId])) setSummary('')
  }

  if (!session.signedIn) {
    return <ReviewEmpty title="Sign in to review shared versions" detail="Reviews are stored on the platform backend and are always bound to an immutable artifact revision." action="Open sign in" onAction={() => setSurface('settings')} />
  }
  if (!project) {
    return <ReviewEmpty title="Select a shared project" detail={error ?? 'No project is selected.'} action="Retry" onAction={() => void refresh()} />
  }

  return (
    <div className="flex h-full flex-col bg-canvas">
      <header className="flex flex-wrap items-start gap-3 border-b border-border bg-panel px-6 py-4 max-sm:px-4">
        <span className="min-w-0 flex-1"><h1 className="text-lg font-semibold text-foreground">Version reviews</h1><p className="mt-1 text-[12px] text-muted-foreground">{project.name} · reviews target exact revision IDs and content hashes</p></span>
        <button type="button" onClick={() => void refresh()} disabled={loading} className="rounded-md border border-border p-2 text-muted-foreground disabled:opacity-50" aria-label="Refresh reviews"><RefreshCw className={cn('size-4', loading && 'animate-spin')} /></button>
      </header>

      {error && <p role="alert" className="border-b border-destructive/20 bg-destructive/10 px-6 py-2 text-[11px] text-destructive">{error}</p>}

      <div className="flex gap-1.5 overflow-x-auto border-b border-border bg-panel px-6 py-2.5 scrollbar-thin max-sm:px-4">
        {(['all', 'pending', 'approved', 'changesRequested'] as ReviewFilter[]).map((item) => <button key={item} type="button" onClick={() => setFilter(item)} className={cn('rounded-md px-2.5 py-1 text-[11px] font-medium', filter === item ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground hover:bg-white/5')}>{item}</button>)}
      </div>

      <div className="grid min-h-0 flex-1 lg:grid-cols-[minmax(0,1fr)_380px]">
        <main className="overflow-y-auto p-5 scrollbar-thin max-sm:p-4">
          {visibleReviews.length === 0 && <p className="rounded-lg border border-dashed border-border bg-panel p-8 text-center text-sm text-faint-foreground">No reviews match this filter.</p>}
          <div className="space-y-3">
            {visibleReviews.map((review) => (
              <article key={review.id} className="rounded-lg border border-border bg-panel p-4">
                <div className="flex flex-wrap items-center gap-2">
                  <span className={cn('inline-flex items-center gap-1 rounded px-2 py-1 text-[10px] font-medium', review.state === 'approved' ? 'bg-success/10 text-success' : review.state === 'changesRequested' ? 'bg-warning/10 text-warning' : 'bg-primary/10 text-primary-bright')}>{review.state === 'approved' ? <Check className="size-3" /> : review.state === 'changesRequested' ? <X className="size-3" /> : <MessageSquare className="size-3" />}{review.state ?? 'pending'}</span>
                  <span className="text-[11px] font-medium text-foreground">{review.reviewer.name}</span>
                  <span className="ml-auto text-[10px] text-faint-foreground">{new Date(review.createdAt).toLocaleString()}</span>
                </div>
                <p className="mt-3 text-sm leading-relaxed text-muted-foreground">{review.summary}</p>
                <div className="mt-3 rounded-md border border-border bg-background px-3 py-2 font-mono text-[10px] text-faint-foreground">{review.target ? `${review.target.title ?? review.target.artifactId} · ${review.target.revisionNumber ? `r${review.target.revisionNumber}` : `revision ${review.target.revisionId.slice(0, 12)}`} · ${review.target.contentHash.slice(0, 12)}` : 'Review target unavailable'}</div>
                {review.state === 'pending' && can('edit') && review.requiredReviewerIds?.includes(session.user.id) && (
                  <div className="mt-3">
                    {review.policy.governanceMode === 'solo'
                      && review.policy.soloSelfReviewOwnerId === session.user.id && (
                      <div role="alert" className="mb-2 rounded-md border border-warning/35 bg-warning/10 p-2.5 text-[10px] leading-relaxed text-warning">
                        <div className="flex items-start gap-1.5">
                          <CircleAlert className="mt-0.5 size-3.5 shrink-0" />
                          <span>You are approving a revision you authored. This decision is recorded as a Solo self-review.</span>
                        </div>
                        <label className="mt-2 flex cursor-pointer items-start gap-1.5 text-foreground">
                          <input
                            type="checkbox"
                            checked={soloReviewConfirmations.has(review.id)}
                            onChange={(event) => setSoloReviewConfirmations((current) => {
                              const next = new Set(current)
                              if (event.target.checked) next.add(review.id)
                              else next.delete(review.id)
                              return next
                            })}
                            className="mt-0.5"
                          />
                          <span>I confirm this Solo self-review and its audit record.</span>
                        </label>
                      </div>
                    )}
                    <textarea
                      value={approvalSummaries[review.id] ?? ''}
                      onChange={(event) => setApprovalSummaries((current) => ({
                        ...current,
                        [review.id]: event.target.value,
                      }))}
                      rows={2}
                      maxLength={4000}
                      placeholder="Approval reason"
                      aria-label="Approval reason"
                      className="mb-2 w-full rounded-md border border-border bg-background p-2 text-[10px] text-foreground"
                    />
                    <div className="flex gap-2">
                      <button
                        type="button"
                        onClick={() => {
                          const approvalSummary = (approvalSummaries[review.id] ?? '').trim()
                          if (!approvalSummary) return
                          void decideReview(
                            review.id,
                            'approve',
                            approvalSummary,
                            review.policy.governanceMode === 'solo'
                              && review.policy.soloSelfReviewOwnerId === session.user.id
                              && soloReviewConfirmations.has(review.id),
                          ).then((approved) => {
                            if (!approved) return
                            setApprovalSummaries((current) => ({ ...current, [review.id]: '' }))
                          })
                        }}
                        disabled={
                          !(approvalSummaries[review.id] ?? '').trim()
                          || (
                            review.policy.governanceMode === 'solo'
                            && review.policy.soloSelfReviewOwnerId === session.user.id
                            && !soloReviewConfirmations.has(review.id)
                          )
                        }
                        className="rounded-md bg-success px-2.5 py-1.5 text-[10px] font-semibold text-success-foreground disabled:opacity-45"
                      >
                        Approve
                      </button>
                      <button type="button" onClick={() => { const reason = window.prompt('Describe the required changes')?.trim(); if (reason) void decideReview(review.id, 'request_changes', reason) }} className="rounded-md border border-border px-2.5 py-1.5 text-[10px] text-muted-foreground">Request changes</button>
                    </div>
                  </div>
                )}
              </article>
            ))}
          </div>
        </main>

        <aside className="overflow-y-auto border-l border-border bg-panel p-5 scrollbar-thin max-lg:border-l-0 max-lg:border-t">
          <h2 className="text-sm font-semibold text-foreground">Submit review</h2>
          <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">Only server revisions listed below can be reviewed. A local unsaved document is never a review target.</p>
          {!can('edit') ? (
            <p className="mt-4 rounded-md border border-border bg-background p-3 text-[11px] text-muted-foreground">Your {project.role} role cannot submit reviews.</p>
          ) : reviewTargets.length === 0 ? (
            <p className="mt-4 rounded-md border border-dashed border-border p-4 text-[11px] text-faint-foreground">No versioned artifacts are available yet.</p>
          ) : (
            <form onSubmit={submit} className="mt-4 space-y-3">
              <label className="block text-[11px] text-muted-foreground">Artifact revision<select value={target?.revisionId ?? ''} onChange={(event) => setTargetRevisionId(event.target.value)} className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground">{reviewTargets.map((item) => <option key={item.revisionId} value={item.revisionId}>{item.title ?? item.artifactId} · {item.revisionNumber ? `r${item.revisionNumber}` : item.revisionId.slice(0, 12)}</option>)}</select></label>
              <label className="block text-[11px] text-muted-foreground">
                Required reviewer
                {isSoloProject ? (
                  <select value={effectiveReviewerId} disabled className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground disabled:opacity-75">
                    {reviewerCandidates.length === 0 && <option value="">No eligible Solo owner</option>}
                    {reviewerCandidates.map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name} · self-review owner</option>)}
                  </select>
                ) : (
                  <select value={reviewerId} onChange={(event) => setReviewerId(event.target.value)} className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground"><option value="">Select reviewer</option>{reviewerCandidates.map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name} · {member.role}</option>)}</select>
                )}
              </label>
              <label className="block text-[11px] text-muted-foreground">Summary<textarea value={summary} onChange={(event) => setSummary(event.target.value)} rows={6} maxLength={4000} className="mt-1.5 w-full rounded-md border border-border bg-background p-2 text-sm text-foreground" /></label>
              <button type="submit" disabled={loading || !summary.trim() || !target || !effectiveReviewerId} className="inline-flex w-full items-center justify-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground disabled:opacity-50">{loading && <Loader2 className="size-3.5 animate-spin" />}Request version review</button>
            </form>
          )}
        </aside>
      </div>
    </div>
  )
}

function ReviewEmpty({ title, detail, action, onAction }: { title: string; detail: string; action: string; onAction: () => void }) {
  return <div className="flex h-full items-center justify-center bg-canvas p-6 text-center"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6"><MessageSquare className="mx-auto size-8 text-primary-bright" /><h1 className="mt-3 text-base font-semibold text-foreground">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{detail}</p><button type="button" onClick={onAction} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground">{action}</button></div></div>
}
