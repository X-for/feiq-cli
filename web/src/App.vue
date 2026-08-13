<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { pathBreadcrumbs, type PathEntry, type PathListing } from './path-picker'

type Contact = {
  ip: string
  name: string
  host?: string
  online: boolean
  last_seen?: string
  count?: number
  unread?: number
}

type Message = {
  id?: string
  time: string
  direction: 'in' | 'out'
  peer_ip: string
  peer_name?: string
  kind: 'msg' | 'file' | 'dir'
  content: string
  saved_path?: string
  operation_id?: string
  status?: string
  error?: string
}

type StreamEvent = Partial<Message> & {
  id: string
  type: 'message' | 'operation' | 'error'
  operation_id?: string
  status?: string
  error?: string
}

const contacts = ref<Contact[]>([])
const current = ref<Contact | null>(null)
const messages = ref<Message[]>([])
const draft = ref('')
const query = ref('')
const loadingHistory = ref(false)
const sending = ref(false)
const connected = ref(false)
const notice = ref('')
const showPathPanel = ref(false)
const pathListing = ref<PathListing | null>(null)
const pathLoading = ref(false)
const selectedPath = ref<PathEntry | null>(null)
const manualPath = ref('')
const serverPathKind = ref<'file' | 'dir'>('file')
const messageList = ref<HTMLElement | null>(null)
let events: EventSource | null = null
let refreshTimer: number | undefined

const filteredContacts = computed(() => {
  const value = query.value.trim().toLowerCase()
  if (!value) return contacts.value
  return contacts.value.filter((contact) =>
    [contact.name, contact.ip, contact.host].some((field) => field?.toLowerCase().includes(value)),
  )
})

const currentTitle = computed(() => current.value?.name || current.value?.host || current.value?.ip || '')
const breadcrumbs = computed(() => pathListing.value
  ? pathBreadcrumbs(pathListing.value.root, pathListing.value.path)
  : [])

async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = options ? await fetch(path, options) : await fetch(path)
  const body = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(body.error || `请求失败 (${response.status})`)
  return body as T
}

async function refreshContacts(discover = false) {
  try {
    if (discover) {
      await api('/api/discover', { method: 'POST' })
      await new Promise((resolve) => window.setTimeout(resolve, 350))
    }
    const fresh = await api<Contact[]>('/api/contacts')
    const unread = new Map(contacts.value.map((item) => [item.ip, item.unread || 0]))
    contacts.value = fresh.map((item) => ({ ...item, unread: unread.get(item.ip) || 0 }))
    if (current.value) {
      current.value = contacts.value.find((item) => item.ip === current.value?.ip) || current.value
    }
  } catch (error) {
    showNotice(error)
  }
}

async function selectContact(contact: Contact) {
  current.value = contact
  contact.unread = 0
  loadingHistory.value = true
  try {
    messages.value = await api<Message[]>(`/api/history?peer=${encodeURIComponent(contact.ip)}`)
    await scrollToBottom()
  } catch (error) {
    showNotice(error)
  } finally {
    loadingHistory.value = false
  }
}

async function sendMessage() {
  if (!current.value || !draft.value.trim() || sending.value) return
  const text = draft.value
  draft.value = ''
  sending.value = true
  try {
    await api('/api/messages', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ to: current.value.ip, text }),
    })
  } catch (error) {
    draft.value = text
    showNotice(error)
  } finally {
    sending.value = false
  }
}

function handleComposerKey(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
    event.preventDefault()
    void sendMessage()
  }
}

async function loadPaths(path = '') {
  pathLoading.value = true
  selectedPath.value = null
  try {
    const query = path ? `?path=${encodeURIComponent(path)}` : ''
    pathListing.value = await api<PathListing>(`/api/paths${query}`)
    manualPath.value = pathListing.value.path
  } catch (error) {
    showNotice(error)
  } finally {
    pathLoading.value = false
  }
}

async function togglePathPicker() {
  if (showPathPanel.value) {
    showPathPanel.value = false
    return
  }
  showPathPanel.value = true
  await loadPaths()
}

function selectPath(entry: PathEntry) {
  selectedPath.value = entry
  manualPath.value = entry.path
  serverPathKind.value = entry.kind
}

function selectCurrentDirectory() {
  if (!pathListing.value) return
  selectPath({
    name: pathListing.value.path.split('/').filter(Boolean).at(-1) || '/',
    path: pathListing.value.path,
    kind: 'dir',
    size: 0,
  })
}

function selectManualPath() {
  const path = manualPath.value.trim()
  if (!path) return showNotice('请输入路径')
  selectedPath.value = {
    name: path.split('/').filter(Boolean).at(-1) || path,
    path,
    kind: serverPathKind.value,
    size: 0,
  }
}

async function sendServerPath() {
  if (!current.value || !selectedPath.value) return
  const selection = selectedPath.value
  try {
    await api('/api/send-path', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ to: current.value.ip, path: selection.path, kind: selection.kind }),
    })
    selectedPath.value = null
    manualPath.value = ''
    showPathPanel.value = false
  } catch (error) {
    showNotice(error)
  }
}

function connectEvents() {
  events?.close()
  events = new EventSource('/api/events')
  events.onopen = () => (connected.value = true)
  events.onerror = () => (connected.value = false)
  events.onmessage = (message) => {
    const event = JSON.parse(message.data) as StreamEvent
    if (event.type === 'error') return showNotice(event.error || '后台发生错误')
    if (event.type === 'operation') {
      const target = messages.value.find((item) => item.operation_id === event.operation_id)
      if (target) {
        target.status = event.status
        target.error = event.error
      }
      return
    }
    if (event.type !== 'message' || !event.peer_ip || !event.direction || !event.kind || !event.content) return
    const incoming = event as Message
    if (current.value?.ip === incoming.peer_ip) {
      messages.value.push(incoming)
      void scrollToBottom()
    } else {
      let contact = contacts.value.find((item) => item.ip === incoming.peer_ip)
      if (!contact) {
        contact = { ip: incoming.peer_ip, name: incoming.peer_name || incoming.peer_ip, online: true, unread: 0 }
        contacts.value.unshift(contact)
      }
      contact.unread = (contact.unread || 0) + 1
    }
  }
}

function downloadURL(message: Message) {
  return `/api/download?path=${encodeURIComponent(message.saved_path || '')}`
}

function messageTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function contactInitial(contact: Contact) {
  return (contact.name || contact.host || contact.ip).slice(0, 1).toUpperCase()
}

function showNotice(value: unknown) {
  notice.value = value instanceof Error ? value.message : String(value)
  window.setTimeout(() => {
    notice.value = ''
  }, 4500)
}

async function scrollToBottom() {
  await nextTick()
  if (messageList.value) messageList.value.scrollTop = messageList.value.scrollHeight
}

onMounted(async () => {
  connectEvents()
  await refreshContacts(true)
  refreshTimer = window.setInterval(() => void refreshContacts(), 30_000)
})

onBeforeUnmount(() => {
  events?.close()
  if (refreshTimer) window.clearInterval(refreshTimer)
})
</script>

<template>
  <main class="app-shell" :class="{ chatting: current }">
    <aside class="contacts-panel">
      <header class="brand-row">
        <div class="brand-mark">F</div>
        <div>
          <h1>feiq-cli Web</h1>
          <p><span class="status-dot" :class="{ online: connected }"></span>{{ connected ? '实时连接' : '正在重连' }}</p>
        </div>
        <button class="icon-button" title="重新发现用户" @click="refreshContacts(true)">↻</button>
      </header>

      <label class="search-box">
        <span>⌕</span>
        <input v-model="query" type="search" placeholder="搜索用户名、主机或 IP" />
      </label>

      <div class="contact-list">
        <button
          v-for="contact in filteredContacts"
          :key="contact.ip"
          class="contact-card"
          :class="{ active: current?.ip === contact.ip }"
          @click="selectContact(contact)"
        >
          <span class="avatar">{{ contactInitial(contact) }}<i :class="{ online: contact.online }"></i></span>
          <span class="contact-copy">
            <strong>{{ contact.name || '未知用户' }}</strong>
            <small>{{ contact.ip }}<template v-if="contact.host"> · {{ contact.host }}</template></small>
          </span>
          <span v-if="contact.unread" class="unread">{{ contact.unread }}</span>
        </button>
        <div v-if="!filteredContacts.length" class="empty-list">
          <span>⌁</span>
          <p>暂未发现用户</p>
          <button @click="refreshContacts(true)">重新扫描局域网</button>
        </div>
      </div>
    </aside>

    <section class="chat-panel">
      <template v-if="current">
        <header class="chat-header">
          <button class="back-button" @click="current = null">‹</button>
          <span class="avatar small">{{ contactInitial(current) }}<i :class="{ online: current.online }"></i></span>
          <div>
            <h2>{{ currentTitle }}</h2>
            <p>{{ current.ip }} · {{ current.online ? '在线' : '本地历史联系人' }}</p>
          </div>
          <button class="header-action" data-test="open-path-picker" @click="togglePathPicker">选择本机路径</button>
        </header>

        <div class="chat-body" data-test="chat-body">
        <section v-if="showPathPanel" class="path-picker" data-test="path-picker" aria-label="服务端路径选择器">
          <div class="path-toolbar">
            <label>
              <span>允许目录</span>
              <select
                :value="pathListing?.root || ''"
                aria-label="允许目录"
                data-test="path-root"
                :disabled="pathLoading"
                @change="loadPaths(($event.target as HTMLSelectElement).value)"
              >
                <option v-for="root in pathListing?.roots || []" :key="root" :value="root">{{ root }}</option>
              </select>
            </label>
            <button
              type="button"
              class="ghost-button"
              data-test="path-parent"
              :disabled="!pathListing?.parent || pathLoading"
              @click="pathListing?.parent && loadPaths(pathListing.parent)"
            >上一级</button>
            <button type="button" class="ghost-button" :disabled="!pathListing || pathLoading" @click="selectCurrentDirectory">选择当前目录</button>
          </div>

          <nav v-if="breadcrumbs.length" class="path-breadcrumbs" aria-label="当前路径">
            <template v-for="(crumb, index) in breadcrumbs" :key="crumb.path">
              <span v-if="index" aria-hidden="true">/</span>
              <button type="button" :disabled="pathLoading" @click="loadPaths(crumb.path)">{{ crumb.label }}</button>
            </template>
          </nav>

          <div class="path-manual">
            <select v-model="serverPathKind" aria-label="手动路径类型">
              <option value="file">文件</option>
              <option value="dir">目录</option>
            </select>
            <input v-model="manualPath" aria-label="手动路径" placeholder="~/Desktop/example" @keyup.enter="selectManualPath" />
            <button type="button" class="ghost-button" @click="loadPaths(manualPath)">定位目录</button>
            <button type="button" class="ghost-button" data-test="select-manual-path" @click="selectManualPath">选择路径</button>
          </div>

          <div class="path-list" aria-live="polite">
            <p v-if="pathLoading" class="path-state">正在读取目录…</p>
            <p v-else-if="pathListing && !pathListing.entries.length" class="path-state">此目录为空</p>
            <div
              v-for="entry in pathListing?.entries || []"
              v-else
              :key="entry.path"
              class="path-entry"
              :class="{ selected: selectedPath?.path === entry.path }"
            >
              <button
                type="button"
                class="path-entry-select"
                :data-test="`path-entry-${entry.kind}`"
                @click="selectPath(entry)"
                @dblclick="entry.kind === 'dir' && loadPaths(entry.path)"
              >
                <span class="path-entry-icon">{{ entry.kind === 'dir' ? '▦' : '▤' }}</span>
                <span class="path-entry-name">{{ entry.name }}</span>
                <small>{{ entry.kind === 'dir' ? '目录' : `${entry.size} B` }}</small>
              </button>
              <button v-if="entry.kind === 'dir'" type="button" class="path-open" @click="loadPaths(entry.path)">打开</button>
            </div>
          </div>

          <div class="path-picker-actions">
            <p>
              <span>已选择</span>
              <strong>{{ selectedPath?.path || '尚未选择文件或目录' }}</strong>
            </p>
            <button type="button" class="ghost-button" @click="showPathPanel = false">取消</button>
            <button type="button" data-test="send-selected-path" :disabled="!selectedPath || pathLoading" @click="sendServerPath">发送</button>
          </div>
        </section>

        <div ref="messageList" class="message-list">
          <div v-if="loadingHistory" class="conversation-placeholder">正在读取聊天记录…</div>
          <div v-else-if="!messages.length" class="conversation-placeholder welcome">
            <span>✦</span>
            <h3>开始与 {{ currentTitle }} 对话</h3>
            <p>消息和附件通过局域网直接传输。</p>
          </div>
          <article
            v-for="(message, index) in messages"
            :key="message.id || `${message.time}-${index}`"
            class="message-row"
            :class="message.direction"
          >
            <div class="bubble" :class="message.kind">
              <div v-if="message.kind !== 'msg'" class="attachment-icon">{{ message.kind === 'dir' ? '▦' : '▤' }}</div>
              <div class="message-body">
                <p>{{ message.content }}</p>
                <a v-if="message.saved_path && message.kind === 'file'" :href="downloadURL(message)">下载文件</a>
                <small>
                  {{ messageTime(message.time) }}
                  <span v-if="message.status"> · {{ message.status }}</span>
                  <span v-if="message.error" class="message-error"> · {{ message.error }}</span>
                </small>
              </div>
            </div>
          </article>
        </div>
        </div>

        <footer class="composer">
          <textarea
            v-model="draft"
            data-test="message-input"
            rows="1"
            placeholder="输入消息；Enter 发送，Shift+Enter 换行"
            @keydown="handleComposerKey"
          ></textarea>
          <button class="send-button" :disabled="!draft.trim() || sending" @click="sendMessage">发送</button>
        </footer>
      </template>

      <div v-else class="empty-chat">
        <div class="signal-art"><span></span><span></span><span></span><b>F</b></div>
        <h2>局域网消息，浏览器中完成</h2>
        <p>从左侧选择一个在线用户，开始发送消息、文件或整个目录。</p>
      </div>
    </section>

    <div v-if="notice" class="toast">{{ notice }}</div>
  </main>
</template>
