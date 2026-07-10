export type PreviewDimensionAxis = 'width' | 'height'

export interface PreviewDimensions {
  width: number
  height: number
}

export type PreviewInspectionMode = 'element' | 'region'

export interface PreviewInspectionRect {
  x: number
  y: number
  width: number
  height: number
}

export interface PreviewWorkspaceAsset {
  path: string
  content: string
}

export interface PreviewRuntimeContext {
  level: 'log' | 'info' | 'warn' | 'error'
  message: string
  kind?: 'console' | 'runtime' | 'unhandled-rejection'
  route?: string
  stack?: string
  source?: string
  line?: number
  column?: number
}

export interface PreviewSelectionContext {
  kind: PreviewInspectionMode
  selector: string
  route?: string
  rect?: PreviewInspectionRect
  text?: string
}

export const PREVIEW_DIMENSION_LIMITS: Record<
  PreviewDimensionAxis,
  { min: number; max: number }
> = {
  width: { min: 280, max: 2560 },
  height: { min: 240, max: 1600 },
}

export const PREVIEW_DEVICE_DIMENSIONS = {
  desktop: { width: 1280, height: 800 },
  tablet: { width: 768, height: 1024 },
  mobile: { width: 390, height: 844 },
} as const satisfies Record<string, PreviewDimensions>

export const PREVIEW_SANDBOX_CSP = "default-src 'none'; base-uri 'none'; form-action 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; connect-src 'none'; img-src data: blob:; media-src data: blob:; font-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'"

const MAX_ROUTE_LENGTH = 512
const ROUTE_ORIGIN = 'https://worksflow-preview.invalid'
const MAX_CONTEXT_TEXT_LENGTH = 4_000
const MAX_ASSET_CONTENT_LENGTH = 7_000_000

const SAFE_ASSET_MIME_TYPES: Readonly<Record<string, string>> = {
  avif: 'image/avif',
  bmp: 'image/bmp',
  gif: 'image/gif',
  ico: 'image/x-icon',
  jpeg: 'image/jpeg',
  jpg: 'image/jpeg',
  png: 'image/png',
  svg: 'image/svg+xml',
  webp: 'image/webp',
  mp3: 'audio/mpeg',
  m4a: 'audio/mp4',
  oga: 'audio/ogg',
  ogg: 'audio/ogg',
  wav: 'audio/wav',
  mp4: 'video/mp4',
  ogv: 'video/ogg',
  webm: 'video/webm',
  otf: 'font/otf',
  ttf: 'font/ttf',
  woff: 'font/woff',
  woff2: 'font/woff2',
}

export function parsePreviewDimension(
  input: string,
  axis: PreviewDimensionAxis,
): number | null {
  const normalized = input.trim().toLowerCase()
  if (!/^\d{2,4}(?:px)?$/.test(normalized)) return null

  const value = Number.parseInt(normalized.replace(/px$/, ''), 10)
  const limits = PREVIEW_DIMENSION_LIMITS[axis]
  if (!Number.isSafeInteger(value) || value < limits.min || value > limits.max) return null
  return value
}

export function normalizePreviewRoute(input: string, currentRoute = '/'): string {
  const fallback = normalizeRouteCandidate(currentRoute) ?? '/'
  const trimmed = Array.from(input)
    .filter((character) => {
      const code = character.charCodeAt(0)
      return code >= 32 && code !== 127
    })
    .join('')
    .trim()
    .slice(0, MAX_ROUTE_LENGTH)
  if (!trimmed) return '/'

  const candidate = trimmed.startsWith('#/') ? trimmed.slice(1) : trimmed
  const normalized = normalizeRouteCandidate(candidate, fallback)
  return normalized ?? fallback
}

export function calculatePreviewFitScale(
  containerWidth: number,
  containerHeight: number,
  dimensions: PreviewDimensions,
  padding = 24,
): number {
  if (
    !Number.isFinite(containerWidth) ||
    !Number.isFinite(containerHeight) ||
    containerWidth <= padding ||
    containerHeight <= padding ||
    dimensions.width <= 0 ||
    dimensions.height <= 0
  ) {
    return 1
  }

  const availableWidth = Math.max(1, containerWidth - padding)
  const availableHeight = Math.max(1, containerHeight - padding)
  return Math.max(
    0.1,
    Math.min(1, availableWidth / dimensions.width, availableHeight / dimensions.height),
  )
}

export function normalizePreviewInspectionRect(
  startX: number,
  startY: number,
  endX: number,
  endY: number,
  viewport?: PreviewDimensions,
): PreviewInspectionRect | null {
  if (![startX, startY, endX, endY].every(Number.isFinite)) return null

  const maxWidth = viewport && Number.isFinite(viewport.width) ? Math.max(0, viewport.width) : Infinity
  const maxHeight = viewport && Number.isFinite(viewport.height) ? Math.max(0, viewport.height) : Infinity
  const left = clampCoordinate(Math.min(startX, endX), maxWidth)
  const top = clampCoordinate(Math.min(startY, endY), maxHeight)
  const right = clampCoordinate(Math.max(startX, endX), maxWidth)
  const bottom = clampCoordinate(Math.max(startY, endY), maxHeight)

  return {
    x: roundCoordinate(left),
    y: roundCoordinate(top),
    width: roundCoordinate(Math.max(0, right - left)),
    height: roundCoordinate(Math.max(0, bottom - top)),
  }
}

export function buildPreviewErrorComposerContext(
  context: PreviewRuntimeContext,
  currentRoute = '/',
) {
  const payload = {
    contextType: 'preview-runtime-error',
    level: context.level,
    kind: context.kind ?? 'console',
    route: normalizePreviewRoute(context.route ?? currentRoute, currentRoute),
    message: clipContextText(context.message),
    ...(context.stack ? { stack: clipContextText(context.stack) } : {}),
    ...(context.source ? { source: clipContextText(context.source, 1_024) } : {}),
    ...(Number.isFinite(context.line) ? { line: context.line } : {}),
    ...(Number.isFinite(context.column) ? { column: context.column } : {}),
  }

  return `Fix this preview runtime error while preserving unrelated behavior.\n\n\`\`\`json\n${JSON.stringify(payload, null, 2)}\n\`\`\`\n\nVerify the fix on the same route.`
}

export function buildPreviewSelectionComposerContext(
  context: PreviewSelectionContext,
  currentRoute = '/',
) {
  const payload = {
    contextType: 'preview-selection',
    selectionKind: context.kind,
    route: normalizePreviewRoute(context.route ?? currentRoute, currentRoute),
    selector: clipContextText(context.selector, 1_024),
    ...(context.rect ? { rect: context.rect } : {}),
    ...(context.text ? { text: clipContextText(context.text, 1_000) } : {}),
  }

  const target = context.kind === 'region' ? 'selected preview region' : 'selected preview element'
  return `Update the ${target} described by this structured context.\n\n\`\`\`json\n${JSON.stringify(payload, null, 2)}\n\`\`\`\n\nDescribe the desired change here: `
}

/**
 * Replaces only relative references that resolve to an explicitly supplied workspace asset.
 * Unsupported, external, oversized, or malformed resources are left untouched and remain
 * blocked by the preview CSP.
 */
export function inlineSafePreviewAssets(
  document: string,
  entryPath: string,
  assets: readonly PreviewWorkspaceAsset[],
) {
  const filesByPath = new Map(
    assets
      .filter((asset) => isSafeRelativeAssetPath(asset.path))
      .map((asset) => [normalizeAssetPath(asset.path), asset]),
  )
  const replaceReference = (reference: string, sourcePath: string) => {
    const resolvedPath = resolveRelativeAssetPath(sourcePath, reference)
    const asset = resolvedPath ? filesByPath.get(resolvedPath) : undefined
    return asset ? workspaceAssetDataUrl(asset) ?? reference : reference
  }
  const replaceCssUrls = (css: string, sourcePath: string) => css.replace(
    /url\(\s*(["']?)([^"')]+)\1\s*\)/gi,
    (match, _quote: string, reference: string) => {
      const replacement = replaceReference(reference.trim(), sourcePath)
      return replacement === reference.trim() ? match : `url("${replacement}")`
    },
  )

  let inlined = document.replace(
    /<style\b([^>]*)>([\s\S]*?)<\/style>/gi,
    (tag, attributes: string, css: string) => {
      const workspacePath = attributes.match(/\bdata-workspace-path=["']([^"']+)["']/i)?.[1]
      const sourcePath = workspacePath && isSafeRelativeAssetPath(workspacePath)
        ? normalizeAssetPath(workspacePath)
        : normalizeAssetPath(entryPath)
      const replaced = replaceCssUrls(css, sourcePath)
      return replaced === css ? tag : `<style${attributes}>${replaced}</style>`
    },
  )

  inlined = inlined.replace(
    /\b(src|poster)=(['"])([^'"]+)\2/gi,
    (match, attribute: string, quote: string, reference: string) => {
      const replacement = replaceReference(reference, entryPath)
      return replacement === reference ? match : `${attribute}=${quote}${replacement}${quote}`
    },
  )

  inlined = inlined.replace(
    /\bsrcset=(['"])([^'"]+)\1/gi,
    (match, quote: string, sourceSet: string) => {
      const candidates = sourceSet.split(',').map((candidate) => {
        const parts = candidate.trim().split(/\s+/)
        const reference = parts.shift()
        if (!reference) return candidate
        return [replaceReference(reference, entryPath), ...parts].join(' ')
      })
      const replacement = candidates.join(', ')
      return replacement === sourceSet ? match : `srcset=${quote}${replacement}${quote}`
    },
  )

  inlined = inlined.replace(
    /<link\b([^>]*?)href=(['"])([^'"]+)\2([^>]*)>/gi,
    (tag, before: string, quote: string, reference: string, after: string) => {
      if (!/\brel\s*=\s*["']?(?:icon|apple-touch-icon|preload)["']?/i.test(`${before} ${after}`)) {
        return tag
      }
      const replacement = replaceReference(reference, entryPath)
      return replacement === reference
        ? tag
        : `<link${before}href=${quote}${replacement}${quote}${after}>`
    },
  )

  return inlined.replace(
    /\bstyle=(['"])([\s\S]*?)\1/gi,
    (match, quote: string, css: string) => {
      const replacement = replaceCssUrls(css, entryPath)
      return replacement === css ? match : `style=${quote}${replacement}${quote}`
    },
  )
}

export function createSandboxPreviewDocument(
  html: string,
  channelId: string,
  inspectionMode: PreviewInspectionMode | null,
) {
  const safeChannel = JSON.stringify(channelId).replace(/</g, '\\u003c')
  const safeInspectionMode = JSON.stringify(inspectionMode)
  const bridge = `<meta http-equiv="Content-Security-Policy" content="${PREVIEW_SANDBOX_CSP}">
<script>
(() => {
  const channelId = ${safeChannel};
  const inspectionMode = ${safeInspectionMode};
  const send = (payload) => parent.postMessage({ source: 'worksflow-preview', channelId, ...payload }, '*');
  const clip = (value, limit = 4000) => String(value ?? '').slice(0, limit);
  const stringify = (value) => {
    try { return clip(typeof value === 'string' ? value : JSON.stringify(value)); }
    catch { return clip(value); }
  };
  const normalizeRoute = (input, current = '/') => {
    const raw = String(input ?? '').replace(/[\\u0000-\\u001f\\u007f]/g, '').trim();
    if (!raw) return '/';
    if (/^(?:javascript|data|vbscript):/i.test(raw)) return current;
    try {
      const base = new URL(current, 'https://worksflow-preview.invalid');
      const candidate = raw.startsWith('#/') ? raw.slice(1) : raw;
      const resolved = new URL(candidate, base);
      if (resolved.protocol !== 'http:' && resolved.protocol !== 'https:') return current;
      return (resolved.pathname + resolved.search + resolved.hash).slice(0, 512) || '/';
    } catch { return current; }
  };
  let virtualRoute = normalizeRoute(location.hash.startsWith('#/') ? location.hash.slice(1) : '/');
  ['log', 'info', 'warn', 'error'].forEach((level) => {
    const original = console[level].bind(console);
    console[level] = (...args) => {
      original(...args);
      send({ type: 'log', level, kind: 'console', route: virtualRoute, message: args.map(stringify).join(' ') });
    };
  });
  addEventListener('error', (event) => send({
    type: 'log',
    level: 'error',
    kind: 'runtime',
    route: virtualRoute,
    message: clip(event.message || 'Runtime error'),
    stack: clip(event.error && event.error.stack),
    filename: clip(event.filename, 1024),
    line: Number.isFinite(event.lineno) ? event.lineno : undefined,
    column: Number.isFinite(event.colno) ? event.colno : undefined,
  }));
  addEventListener('unhandledrejection', (event) => send({
    type: 'log',
    level: 'error',
    kind: 'unhandled-rejection',
    route: virtualRoute,
    message: stringify(event.reason),
    stack: clip(event.reason && event.reason.stack),
  }));
  const escapeSelector = (value) => window.CSS && CSS.escape
    ? CSS.escape(value)
    : String(value).replace(/[^a-zA-Z0-9_-]/g, (character) => String.fromCharCode(92) + character);
  const selectorFor = (element) => {
    if (!(element instanceof Element)) return 'body';
    if (element.id) return '#' + escapeSelector(element.id);
    const testId = element.getAttribute('data-testid');
    if (testId) return '[data-testid=' + escapeSelector(testId) + ']';
    const parts = [];
    let current = element;
    while (current && current !== document.documentElement && parts.length < 5) {
      let part = current.tagName.toLowerCase();
      const classNames = Array.from(current.classList).slice(0, 2).map(escapeSelector);
      if (classNames.length) part += '.' + classNames.join('.');
      const siblings = current.parentElement
        ? Array.from(current.parentElement.children).filter((sibling) => sibling.tagName === current.tagName)
        : [];
      if (siblings.length > 1) part += ':nth-of-type(' + (siblings.indexOf(current) + 1) + ')';
      parts.unshift(part);
      current = current.parentElement;
    }
    return parts.join(' > ') || element.tagName.toLowerCase();
  };
  const rectPayload = (left, top, right, bottom) => ({
    x: Math.round(Math.max(0, Math.min(left, right)) * 100) / 100,
    y: Math.round(Math.max(0, Math.min(top, bottom)) * 100) / 100,
    width: Math.round(Math.max(0, Math.abs(right - left)) * 100) / 100,
    height: Math.round(Math.max(0, Math.abs(bottom - top)) * 100) / 100,
  });
  let regionStart = null;
  let regionOverlay = null;
  let regionCaptureTarget = null;
  let regionPointerId = null;
  const removeRegionOverlay = () => {
    try {
      if (
        regionCaptureTarget &&
        regionPointerId !== null &&
        regionCaptureTarget.hasPointerCapture(regionPointerId)
      ) regionCaptureTarget.releasePointerCapture(regionPointerId);
    } catch {}
    if (regionOverlay) regionOverlay.remove();
    regionOverlay = null;
    regionStart = null;
    regionCaptureTarget = null;
    regionPointerId = null;
  };
  if (inspectionMode) document.documentElement.style.cursor = 'crosshair';
  if (inspectionMode === 'element') {
    addEventListener('click', (event) => {
      if (!(event.target instanceof Element)) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      const bounds = event.target.getBoundingClientRect();
      send({
        type: 'selected',
        selector: selectorFor(event.target),
        text: clip((event.target.textContent || '').trim(), 1000),
        route: virtualRoute,
        rect: rectPayload(bounds.left, bounds.top, bounds.right, bounds.bottom),
      });
    }, true);
  }
  if (inspectionMode === 'region') {
    addEventListener('pointerdown', (event) => {
      if (event.button !== 0) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      removeRegionOverlay();
      regionStart = { x: event.clientX, y: event.clientY };
      if (event.target instanceof Element && typeof event.target.setPointerCapture === 'function') {
        try {
          event.target.setPointerCapture(event.pointerId);
          regionCaptureTarget = event.target;
          regionPointerId = event.pointerId;
        } catch {}
      }
      regionOverlay = document.createElement('div');
      regionOverlay.setAttribute('aria-hidden', 'true');
      Object.assign(regionOverlay.style, {
        position: 'fixed', zIndex: '2147483647', pointerEvents: 'none',
        border: '2px solid #1488fc', background: 'rgba(20, 136, 252, 0.14)',
        left: event.clientX + 'px', top: event.clientY + 'px', width: '0', height: '0',
      });
      document.documentElement.append(regionOverlay);
    }, true);
    addEventListener('pointermove', (event) => {
      if (!regionStart || !regionOverlay) return;
      event.preventDefault();
      const rect = rectPayload(regionStart.x, regionStart.y, event.clientX, event.clientY);
      Object.assign(regionOverlay.style, {
        left: rect.x + 'px', top: rect.y + 'px', width: rect.width + 'px', height: rect.height + 'px',
      });
    }, true);
    const finishRegion = (event) => {
      if (!regionStart) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      const rect = rectPayload(regionStart.x, regionStart.y, event.clientX, event.clientY);
      const centerX = Math.min(innerWidth - 1, Math.max(0, rect.x + rect.width / 2));
      const centerY = Math.min(innerHeight - 1, Math.max(0, rect.y + rect.height / 2));
      const target = document.elementFromPoint(centerX, centerY) || document.body;
      removeRegionOverlay();
      send({
        type: 'region-selected',
        selector: selectorFor(target),
        text: clip((target.textContent || '').trim(), 1000),
        route: virtualRoute,
        rect,
      });
    };
    addEventListener('pointerup', finishRegion, true);
    addEventListener('pointercancel', removeRegionOverlay, true);
  }
  const nativePushState = history.pushState.bind(history);
  const nativeReplaceState = history.replaceState.bind(history);
  const publishRoute = () => {
    send({ type: 'route', route: virtualRoute, location: location.href });
    dispatchEvent(new CustomEvent('worksflow:routechange', { detail: { route: virtualRoute } }));
  };
  const commitRoute = (next, replace, state, title) => {
    virtualRoute = normalizeRoute(next, virtualRoute);
    try {
      const update = replace ? nativeReplaceState : nativePushState;
      update(state ?? history.state, title ?? '', '#' + virtualRoute);
    } catch {
      location.hash = virtualRoute;
    }
    publishRoute();
  };
  history.pushState = (state, title, url) => commitRoute(url ?? virtualRoute, false, state, title);
  history.replaceState = (state, title, url) => commitRoute(url ?? virtualRoute, true, state, title);
  const syncRouteFromLocation = () => {
    virtualRoute = normalizeRoute(location.hash.startsWith('#/') ? location.hash.slice(1) : virtualRoute, virtualRoute);
    publishRoute();
  };
  addEventListener('hashchange', syncRouteFromLocation);
  addEventListener('popstate', syncRouteFromLocation);
  addEventListener('message', (event) => {
    const data = event.data;
    if (
      event.source !== parent ||
      !data ||
      data.source !== 'worksflow-preview-host' ||
      data.channelId !== channelId ||
      data.type !== 'navigate'
    ) return;
    commitRoute(data.route, Boolean(data.replace), history.state, document.title);
  });
  addEventListener('click', (event) => {
    if (event.defaultPrevented || !(event.target instanceof Element)) return;
    const anchor = event.target.closest('a[href]');
    if (!anchor || anchor.hasAttribute('download') || anchor.target === '_blank') return;
    const href = anchor.getAttribute('href');
    if (!href || /^(?:mailto|tel|javascript|data):/i.test(href)) return;
    event.preventDefault();
    history.pushState({}, '', href);
  }, true);
  addEventListener('keydown', (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'r') {
      event.preventDefault();
      send({ type: 'reload' });
    }
    if (event.key === 'Escape') {
      removeRegionOverlay();
      send({ type: 'escape' });
    }
  });
  try { nativeReplaceState(history.state, document.title, '#' + virtualRoute); } catch {}
  send({ type: 'log', level: 'info', kind: 'console', route: virtualRoute, message: 'Preview runtime connected' });
  send({ type: 'ready', route: virtualRoute, location: location.href });
})();
</script>`
  if (/<head(?:\s[^>]*)?>/i.test(html)) {
    return html.replace(/<head(?:\s[^>]*)?>/i, (match) => `${match}\n${bridge}`)
  }
  return `${bridge}\n${html}`
}

function normalizeRouteCandidate(input: string, baseRoute = '/'): string | null {
  try {
    const base = new URL(baseRoute, ROUTE_ORIGIN)
    const resolved = new URL(input.replace(/\\/g, '/'), base)
    if (resolved.protocol !== 'http:' && resolved.protocol !== 'https:') return null

    const route = `${resolved.pathname}${resolved.search}${resolved.hash}`
    return route.startsWith('/') ? route.slice(0, MAX_ROUTE_LENGTH) : `/${route}`.slice(0, MAX_ROUTE_LENGTH)
  } catch {
    return null
  }
}

function clampCoordinate(value: number, maximum: number) {
  return Math.max(0, Math.min(maximum, value))
}

function roundCoordinate(value: number) {
  return Math.round(value * 100) / 100
}

function clipContextText(value: string, limit = MAX_CONTEXT_TEXT_LENGTH) {
  return Array.from(String(value)).slice(0, limit).join('')
}

function normalizeAssetPath(path: string) {
  return path.replace(/\\/g, '/').replace(/^\.\//, '')
}

function isSafeRelativeAssetPath(path: string) {
  const normalized = normalizeAssetPath(path)
  return Boolean(
    normalized &&
    !normalized.startsWith('/') &&
    !/^[a-z][a-z\d+.-]*:/i.test(normalized) &&
    !normalized.split('/').some((segment) => segment === '..' || segment === ''),
  )
}

function resolveRelativeAssetPath(sourcePath: string, reference: string) {
  const cleanReference = reference.split(/[?#]/, 1)[0]
  if (
    !cleanReference ||
    cleanReference.startsWith('#') ||
    cleanReference.startsWith('//') ||
    /^[a-z][a-z\d+.-]*:/i.test(cleanReference)
  ) return null

  let decodedReference: string
  try {
    decodedReference = decodeURIComponent(cleanReference)
  } catch {
    return null
  }
  const workspaceRootReference = decodedReference.startsWith('/')
  const segments = [
    ...(workspaceRootReference ? [] : normalizeAssetPath(sourcePath).split('/').slice(0, -1)),
    ...decodedReference.replace(/\\/g, '/').replace(/^\//, '').split('/'),
  ]
  const resolved: string[] = []
  for (const segment of segments) {
    if (!segment || segment === '.') continue
    if (segment === '..') {
      if (resolved.length === 0) return null
      resolved.pop()
      continue
    }
    resolved.push(segment)
  }
  const path = resolved.join('/')
  return isSafeRelativeAssetPath(path) ? path : null
}

function workspaceAssetDataUrl(asset: PreviewWorkspaceAsset) {
  if (asset.content.length > MAX_ASSET_CONTENT_LENGTH) return null
  const extension = asset.path.split('.').pop()?.toLowerCase() ?? ''
  const expectedMime = SAFE_ASSET_MIME_TYPES[extension]
  if (!expectedMime) return null

  const trimmed = asset.content.trim()
  if (trimmed.startsWith('data:')) {
    const match = trimmed.match(/^data:([^;,]+)(?:;charset=[^;,]+)?(?:;base64)?,/i)
    if (!match || !isCompatibleAssetMime(expectedMime, match[1])) return null
    if (/["'<>\r\n]/.test(trimmed)) return null
    return trimmed
  }
  const encodedMatch = trimmed.match(/^base64[:,]([a-z\d+/=]+)$/i)
  if (encodedMatch && encodedMatch[1].length % 4 === 0) {
    return `data:${expectedMime};base64,${encodedMatch[1]}`
  }
  if (expectedMime === 'image/svg+xml' && /^<svg\b/i.test(trimmed)) {
    return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(trimmed)}`
  }
  return null
}

function isCompatibleAssetMime(expected: string, actual: string) {
  const normalizedActual = actual.toLowerCase()
  if (normalizedActual === expected) return true
  return expected === 'image/jpeg' && normalizedActual === 'image/jpg'
}
