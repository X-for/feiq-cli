import { describe, expect, it } from 'vitest'

import { pathBreadcrumbs } from './path-picker'

describe('pathBreadcrumbs', () => {
  it('does not emit ancestors above a nested root', () => {
    expect(pathBreadcrumbs('/Users/zaq', '/Users/zaq/Documents/images')).toEqual([
      { label: 'zaq', path: '/Users/zaq' },
      { label: 'Documents', path: '/Users/zaq/Documents' },
      { label: 'images', path: '/Users/zaq/Documents/images' },
    ])
  })

  it('returns only the root crumb at the configured root', () => {
    expect(pathBreadcrumbs('/Users/zaq', '/Users/zaq')).toEqual([
      { label: 'zaq', path: '/Users/zaq' },
    ])
  })

  it('does not derive breadcrumbs from a path outside the configured root', () => {
    expect(pathBreadcrumbs('/Users/zaq', '/Users/other')).toEqual([
      { label: 'zaq', path: '/Users/zaq' },
    ])
  })

  it('supports the filesystem root without emitting an empty label', () => {
    expect(pathBreadcrumbs('/', '/Users/zaq')).toEqual([
      { label: '/', path: '/' },
      { label: 'Users', path: '/Users' },
      { label: 'zaq', path: '/Users/zaq' },
    ])
  })
})
