<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'

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
const serverPath = ref('')
const serverPathKind = ref<'file' | 'dir'>('file')
const pendingFiles = ref<File[]>([])
const pendingKind = ref<'file' | 'dir'>('file')
const messageList = ref<HTMLElement | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const directoryInput = ref<HTMLInputElement | null>(null)
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

async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(path, options)
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

function chooseFiles(kind: 'file' | 'dir') {
  if (!current.value) return showNotice('请先选择联系人')
  if (kind === 'file') fileInput.value?.click()
  else directoryInput.value?.click()
}

function selectedFiles(event: Event, kind: 'file' | 'dir') {
  const input = event.target as HTMLInputElement
  pendingFiles.value = Array.from(input.files || [])
  pendingKind.value = kind
  input.value = ''
}

function handlePaste(event: ClipboardEvent) {
  const images = Array.from(event.clipboardData?.files || []).filter((file) => file.type.startsWith('image/'))
  if (!images.length) return
  event.preventDefault()
  pendingFiles.value = images.slice(0, 1)
  pendingKind.value = 'file'
}

async function uploadPending() {
  if (!current.value || !pendingFiles.value.length) return
  const form = new FormData()
  form.append('to', current.value.ip)
  form.append('kind', pendingKind.value)
  for (const file of pendingFiles.value) {
    form.append('files', file, file.name)
    form.append('paths', (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name)
  }
  const files = pendingFiles.value
  pendingFiles.value = []
  try {
    await api('/api/upload', { method: 'POST', body: form })
  } catch (error) {
    pendingFiles.value = files
    showNotice(error)
  }
}

async function sendServerPath() {
  if (!current.value || !serverPath.value.trim()) return
  try {
    await api('/api/send-path', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ to: current.value.ip, path: serverPath.value, kind: serverPathKind.value }),
    })
    serverPath.value = ''
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
          <button class="header-action" @click="showPathPanel = !showPathPanel">发送本机路径</button>
        </header>

        <div v-if="showPathPanel" class="path-panel">
          <select v-model="serverPathKind" aria-label="路径类型">
            <option value="file">文件</option>
            <option value="dir">目录</option>
          </select>
          <input v-model="serverPath" placeholder="~/Desktop/example" @keyup.enter="sendServerPath" />
          <button @click="sendServerPath">发送</button>
        </div>

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

        <div v-if="pendingFiles.length" class="pending-upload">
          <div>
            <strong>{{ pendingKind === 'dir' ? '待发送目录' : '待发送文件' }}</strong>
            <span>{{ pendingFiles.length === 1 ? pendingFiles[0].name : `${pendingFiles.length} 个文件` }}</span>
          </div>
          <button class="ghost-button" @click="pendingFiles = []">取消</button>
          <button @click="uploadPending">发送</button>
        </div>

        <footer class="composer">
          <div class="composer-actions">
            <button title="选择文件" @click="chooseFiles('file')">＋ 文件</button>
            <button title="选择目录" @click="chooseFiles('dir')">▦ 目录</button>
          </div>
          <textarea
            v-model="draft"
            rows="1"
            placeholder="输入消息；Enter 发送，Shift+Enter 换行，也可以粘贴图片"
            @keydown="handleComposerKey"
            @paste="handlePaste"
          ></textarea>
          <button class="send-button" :disabled="!draft.trim() || sending" @click="sendMessage">发送</button>
          <input ref="fileInput" class="hidden-input" type="file" @change="selectedFiles($event, 'file')" />
          <input ref="directoryInput" class="hidden-input" type="file" webkitdirectory multiple @change="selectedFiles($event, 'dir')" />
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
