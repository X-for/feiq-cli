export type PathEntry = {
  name: string
  path: string
  kind: 'file' | 'dir'
  size: number
}

export type PathListing = {
  path: string
  root: string
  roots: string[]
  parent?: string
  entries: PathEntry[]
}

export type PathBreadcrumb = {
  label: string
  path: string
}

function normalizePath(path: string) {
  if (path === '/') return '/'
  return path.replace(/\/+$/, '')
}

function basename(path: string) {
  if (path === '/') return '/'
  const parts = path.split('/').filter(Boolean)
  return parts.at(-1) || path
}

export function pathBreadcrumbs(root: string, path: string): PathBreadcrumb[] {
  const normalizedRoot = normalizePath(root)
  const normalizedPath = normalizePath(path)
  const crumbs: PathBreadcrumb[] = [{ label: basename(normalizedRoot), path: normalizedRoot }]

  if (normalizedPath === normalizedRoot) return crumbs
  if (normalizedRoot !== '/' && !normalizedPath.startsWith(`${normalizedRoot}/`)) return crumbs
  if (normalizedRoot === '/' && !normalizedPath.startsWith('/')) return crumbs
  const relative = normalizedRoot === '/'
    ? normalizedPath.slice(1)
    : normalizedPath.slice(normalizedRoot.length + 1)
  if (!relative || relative.startsWith('../') || relative === '..') return crumbs

  let current = normalizedRoot
  for (const segment of relative.split('/').filter(Boolean)) {
    current = current === '/' ? `/${segment}` : `${current}/${segment}`
    crumbs.push({ label: segment, path: current })
  }
  return crumbs
}
