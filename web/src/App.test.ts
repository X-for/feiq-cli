import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App.vue'

class EventSourceMock {
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null

  close() {}
}

const contact = {
  ip: '192.168.110.150',
  name: 'AQ',
  online: true,
}

const listing = {
  path: '/Users/zaq',
  root: '/Users/zaq',
  roots: ['/Users/zaq'],
  entries: [
    { name: 'Documents', path: '/Users/zaq/Documents', kind: 'dir', size: 0 },
    { name: 'photo.png', path: '/Users/zaq/photo.png', kind: 'file', size: 128 },
  ],
}

function response(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  }))
}

describe('App server path picker', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', EventSourceMock)
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/discover') return response({ accepted: true }, 202)
      if (url === '/api/contacts') return response([contact])
      if (url.startsWith('/api/history')) return response([])
      if (url.startsWith('/api/paths')) return response(listing)
      if (url === '/api/send-path') return response({ accepted: true }, 202)
      return response({ error: 'unexpected request' }, 404)
    }))
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  async function mountConversation() {
    const wrapper = mount(App)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(350)
    await flushPromises()
    await wrapper.get('.contact-card').trigger('click')
    await flushPromises()
    return wrapper
  }

  it('keeps cached contacts available when discovery fails', async () => {
    vi.mocked(fetch).mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/discover') return response({ error: 'broadcast unavailable' }, 500)
      if (url === '/api/contacts') return response([contact])
      return response({ error: 'unexpected request' }, 404)
    })

    const wrapper = mount(App)
    await flushPromises()

    expect(wrapper.find('.contact-card').exists()).toBe(true)
    expect(wrapper.text()).toContain('AQ')
    wrapper.unmount()
  })

  it('removes browser upload controls and opens the server picker', async () => {
    const wrapper = await mountConversation()

    expect(wrapper.text()).not.toContain('＋ 文件')
    expect(wrapper.text()).not.toContain('▦ 目录')
    expect(wrapper.get('[data-test="message-input"]').element).toBeInstanceOf(HTMLTextAreaElement)
    await wrapper.get('[data-test="open-path-picker"]').trigger('click')
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/paths')
    expect(wrapper.find('[data-test="path-picker"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="message-input"]').element).toBeInstanceOf(HTMLTextAreaElement)
    expect(wrapper.get('[data-test="path-picker"]').element.parentElement).toBe(
      wrapper.get('[data-test="chat-body"]').element,
    )
    expect(wrapper.get('[data-test="message-input"]').element.closest('.chat-body')).toBeNull()
    wrapper.unmount()
  })

  it('selects a server file and lets the server determine its kind', async () => {
    const wrapper = await mountConversation()
    await wrapper.get('[data-test="open-path-picker"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="path-entry-file"]').trigger('click')
    await wrapper.get('[data-test="send-selected-path"]').trigger('click')
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/send-path', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        to: contact.ip,
        path: '/Users/zaq/photo.png',
      }),
    }))
    wrapper.unmount()
  })

  it('sends a manually entered path without guessing its kind', async () => {
    const wrapper = await mountConversation()
    await wrapper.get('[data-test="open-path-picker"]').trigger('click')
    await flushPromises()

    await wrapper.get('[aria-label="手动路径"]').setValue('/Users/zaq/Documents')
    await wrapper.get('[data-test="select-manual-path"]').trigger('click')
    await wrapper.get('[data-test="send-selected-path"]').trigger('click')
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/send-path', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        to: contact.ip,
        path: '/Users/zaq/Documents',
      }),
    }))
    wrapper.unmount()
  })
})
