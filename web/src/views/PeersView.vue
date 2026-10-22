<template>
  <div class="peers-page" :class="{ 'in-chat': !!dmPeer }">
    <!-- ===== Left: peer contact list ===== -->
    <aside class="peers-sidebar">
      <div class="peers-sidebar-head">
        <div>
          <h2 class="peers-title">{{ t('peers.title') }}</h2>
          <n-text depth="3" style="font-size: 12px;">{{ t('peers.subtitle') }}</n-text>
        </div>
        <n-space :size="4">
          <n-button quaternary :title="t('peers.accessTitle')" @click="showIdentity = true">
            <template #icon><n-icon><IdCardOutline /></n-icon></template>
          </n-button>
          <n-button quaternary :loading="loading" :title="t('common.refreshed')" @click="load">
            <template #icon><n-icon><RefreshOutline /></n-icon></template>
          </n-button>
        </n-space>
      </div>

      <n-button block dashed @click="openAdd">
        <template #icon><n-icon><AddOutline /></n-icon></template>
        {{ t('peers.add') }}
      </n-button>

      <div class="peers-list">
        <n-empty v-if="!peers.length" :description="t('peers.empty')" size="small">
          <template #extra>
            <n-text depth="3" style="font-size: 12px;">{{ t('peers.emptyHint') }}</n-text>
          </template>
        </n-empty>
        <div
          v-for="p in peers"
          :key="p.name"
          class="peer-item"
          :class="{ active: selectedPeer === p.name }"
          @click="selectPeer(p)"
        >
          <div class="peer-item-top">
            <span class="peer-item-name">{{ p.name }}</span>
            <n-tag :type="p.has_token ? 'success' : 'default'" size="tiny" round>
              {{ p.has_token ? t('peers.hasToken') : t('peers.noToken') }}
            </n-tag>
          </div>
          <div class="peer-item-preview">{{ previewOf(p) }}</div>
          <div class="peer-item-bottom">
            <n-text code style="font-size: 11px;">{{ p.base_url }}</n-text>
            <span class="peer-item-time">{{ lastTimeOf(p) }}</span>
          </div>
        </div>
      </div>
    </aside>

    <!-- ===== Right: conversation with the selected peer's bot ===== -->
    <main class="peers-chat">
      <template v-if="dmPeer">
        <div class="peers-chat-head">
          <div class="peers-chat-head-left">
            <!-- Mobile only: back to the contact list. -->
            <n-button quaternary class="peers-chat-back" :title="t('common.back')" @click="leaveChat">
              <template #icon><n-icon><ArrowBackOutline /></n-icon></template>
            </n-button>
            <div class="peers-chat-title">
              <span class="peers-chat-name">{{ dmPeer.name }}</span>
              <n-text code depth="3" style="font-size: 12px;">{{ dmPeer.base_url }}</n-text>
            </div>
          </div>
          <div class="peers-chat-tools">
            <n-button
              quaternary
              :loading="botsLoading"
              :title="t('peers.dmBotRefresh')"
              @click="loadPeerBots"
            >
              <template #icon><n-icon><RefreshOutline /></n-icon></template>
            </n-button>
            <n-popconfirm @positive-click="removePeer(dmPeer)">
              <template #trigger>
                <n-button quaternary type="error" :title="t('peers.deleteConfirm', { name: dmPeer.name })">
                  <template #icon><n-icon><TrashOutline /></n-icon></template>
                </n-button>
              </template>
              {{ t('peers.deleteConfirm', { name: dmPeer.name }) }}
            </n-popconfirm>
          </div>
        </div>

        <!-- Bot picker as tappable chips: all advertised bots stay visible, the
             active one is highlighted. Falls back to a manual input when the
             remote listing fails or is empty. -->
        <div class="peers-chat-bots">
          <template v-if="botOptions.length">
            <button
              v-for="b in botOptions"
              :key="b.value"
              type="button"
              class="peer-bot-chip"
              :class="{ active: dmForm.bot === b.value }"
              @click="dmForm.bot = b.value"
            >
              <span class="peer-bot-avatar">{{ botInitial(b.value) }}</span>
              <span class="peer-bot-label">{{ b.label }}</span>
            </button>
          </template>
          <n-text v-else-if="botsLoading" depth="3" style="font-size: 12px;">
            {{ t('peers.dmBotLoading') }}
          </n-text>
          <template v-else>
            <n-text type="warning" style="font-size: 12px;">
              {{ botsError ? t('peers.dmBotLoadFailed') : t('peers.dmBotNone') }}
            </n-text>
            <n-input
              v-model:value="dmForm.bot"
              size="small"
              :placeholder="t('peers.dmBotPlaceholder')"
              style="width: 240px;"
            />
          </template>
          <n-popover v-model:show="showCustomBot" trigger="click" placement="bottom-start">
            <template #trigger>
              <button type="button" class="peer-bot-chip add">
                + {{ t('peers.dmCustomBot') }}
              </button>
            </template>
            <div class="peer-bot-custom">
              <n-input
                v-model:value="customBot"
                size="small"
                :placeholder="t('peers.dmBotPlaceholder')"
                @keyup.enter="applyCustomBot"
              />
              <n-text depth="3" style="font-size: 12px;">{{ t('peers.dmBotHint') }}</n-text>
              <n-button size="small" type="primary" @click="applyCustomBot">
                {{ t('common.confirm') }}
              </n-button>
            </div>
          </n-popover>
        </div>

        <div ref="dmListEl" class="peers-chat-list">
          <div v-if="!dmForm.bot" class="peers-chat-empty">
            <n-text depth="3">{{ t('peers.dmPickBot') }}</n-text>
          </div>
          <div v-else-if="!chatHistory.length && !waiting" class="peers-chat-empty">
            <n-text depth="3">{{ t('peers.dmEmpty') }}</n-text>
          </div>
          <template v-for="m in chatHistory" :key="m.id">
            <div v-if="m.role === 'user'" class="peer-bubble-row user">
              <div class="peer-bubble-meta">{{ fmtTime(m.ts) }}</div>
              <div class="peer-bubble user">{{ m.content }}</div>
            </div>
            <div v-else-if="m.role === 'bot'" class="peer-bubble-row bot">
              <div class="peer-bubble bot">
                <div class="peer-md" v-html="renderMd(m.content)"></div>
                <div class="peer-bubble-foot">
                  <span>{{ fmtTime(m.ts) }}</span>
                  <n-button text size="tiny" :title="t('peers.copy')" @click="copy(m.content)">
                    <template #icon><n-icon><CopyOutline /></n-icon></template>
                  </n-button>
                </div>
              </div>
            </div>
            <div v-else class="peer-bubble-row bot">
              <div class="peer-bubble error">
                <div class="peer-err-title">{{ t('peers.dmFailed') }}</div>
                <div class="peer-err-body">{{ m.content }}</div>
                <n-button size="tiny" type="warning" secondary @click="retryLast">
                  {{ t('peers.dmRetry') }}
                </n-button>
              </div>
            </div>
          </template>
          <div v-if="waiting" class="peer-bubble-row bot">
            <div class="peer-bubble bot typing">
              <span class="peer-dot"></span><span class="peer-dot"></span><span class="peer-dot"></span>
              <span class="peer-wait">
                {{ t('peers.dmWaiting') }}
                <n-text depth="3" style="font-size: 12px;">
                  {{ t('peers.dmWaitingElapsed', { s: waitSeconds }) }}
                </n-text>
              </span>
            </div>
          </div>
        </div>

        <div class="peers-chat-composer">
          <n-input
            v-model:value="dmForm.message"
            type="textarea"
            :autosize="{ minRows: 1, maxRows: 5 }"
            :placeholder="t('peers.dmMessagePlaceholder')"
            :disabled="!dmForm.bot"
            :input-props="{ onKeydown: onComposerKeydown }"
          />
          <div class="peers-composer-foot">
            <n-text class="peers-hint-text" depth="3" style="font-size: 12px;">{{ t('peers.dmHint') }}</n-text>
            <n-space align="center" :size="8">
              <n-popconfirm @positive-click="clearHistory">
                <template #trigger>
                  <n-button quaternary size="small" :disabled="waiting || !chatHistory.length">
                    {{ t('peers.dmClear') }}
                  </n-button>
                </template>
                {{ t('peers.dmClearConfirm') }}
              </n-popconfirm>
              <n-button type="primary" :disabled="!canSend" :loading="waiting" @click="sendDM()">
                {{ t('peers.send') }}
              </n-button>
            </n-space>
          </div>
        </div>
      </template>

      <div v-else class="peers-chat-empty">
        <n-empty :description="peers.length ? t('peers.selectPeerHint') : t('peers.empty')">
          <template v-if="!peers.length" #extra>
            <n-button type="primary" @click="openAdd">
              <template #icon><n-icon><AddOutline /></n-icon></template>
              {{ t('peers.add') }}
            </n-button>
          </template>
        </n-empty>
      </div>
    </main>

    <!-- ===== This machine's identity: what a remote operator needs to register us ===== -->
    <n-drawer v-model:show="showIdentity" :width="420">
      <n-drawer-content :title="t('peers.identityTitle')" closable>
        <n-descriptions :column="1" label-placement="left" size="small">
          <n-descriptions-item :label="t('peers.instanceId')">
            <n-space align="center" :size="8">
              <n-text code>{{ overview.instance_id || '—' }}</n-text>
              <n-button v-if="overview.instance_id" size="tiny" quaternary @click="copy(overview.instance_id)">
                <template #icon><n-icon><CopyOutline /></n-icon></template>
                {{ t('peers.copy') }}
              </n-button>
            </n-space>
          </n-descriptions-item>
          <n-descriptions-item :label="t('peers.magicHome')">
            <n-text code>{{ overview.magic_home || '—' }}</n-text>
          </n-descriptions-item>
        </n-descriptions>
        <n-text class="peer-hint" depth="3" style="font-size: 12px;">{{ t('peers.identityHint') }}</n-text>
      </n-drawer-content>
    </n-drawer>

    <!-- Add peer -->
    <n-modal
      v-model:show="showAdd"
      preset="card"
      class="modal-responsive"
      style="width: 520px; max-width: 96vw;"
      :title="t('peers.addTitle')"
    >
      <n-form label-placement="top">
        <n-form-item :label="t('peers.name')">
          <n-input v-model:value="form.name" :placeholder="t('peers.namePlaceholder')" />
          <template #feedback>
            <n-text depth="3" style="font-size: 12px;">{{ t('peers.nameHint') }}</n-text>
          </template>
        </n-form-item>
        <n-form-item :label="t('peers.baseUrl')">
          <n-input v-model:value="form.base_url" :placeholder="t('peers.baseUrlPlaceholder')" />
          <template #feedback>
            <n-text depth="3" style="font-size: 12px;">{{ t('peers.baseUrlHint') }}</n-text>
          </template>
        </n-form-item>
        <n-form-item :label="t('peers.token')">
          <n-input
            v-model:value="form.token"
            type="password"
            show-password-on="click"
            :placeholder="t('peers.tokenPlaceholder')"
          />
          <template #feedback>
            <n-text depth="3" style="font-size: 12px;">{{ t('peers.tokenHint') }}</n-text>
          </template>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showAdd = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :loading="saving" @click="savePeer">{{ t('common.save') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useMessage } from 'naive-ui'
import {
  NButton, NDescriptions, NDescriptionsItem, NDrawer, NDrawerContent, NEmpty, NForm, NFormItem,
  NIcon, NInput, NModal, NPopover, NPopconfirm, NSpace, NTag, NText,
} from 'naive-ui'
import { AddOutline, ArrowBackOutline, CopyOutline, IdCardOutline, RefreshOutline, TrashOutline } from '@vicons/ionicons5'
import { useI18n } from 'vue-i18n'
import { marked } from 'marked'
import { addPeer, deletePeer, getPeers, listPeerBots, sendPeerDM, type PeerBotInfo, type PeerInfo } from '@/api/peers'

const { t } = useI18n()
const message = useMessage()

const loading = ref(false)
const saving = ref(false)
const overview = ref<{ instance_id: string; magic_home: string }>({ instance_id: '', magic_home: '' })
const peers = ref<PeerInfo[]>([])

const showAdd = ref(false)
const form = reactive({ name: '', base_url: '', token: '' })
const showIdentity = ref(false)

const selectedPeer = ref<string | null>(null)
const dmPeer = ref<PeerInfo | null>(null)
const dmForm = reactive({ bot: '', message: '' })

// ===== Peer DM chat =====
// A relayed DM drives a full agent turn on the remote machine and can take
// minutes, so the UX is chat-shaped: one request in flight at a time, a
// per-bot conversation history kept in localStorage, and a retryable error
// bubble instead of a transient toast.
interface PeerChatMsg {
  id: string
  role: 'user' | 'bot' | 'error'
  content: string
  ts: number
}

const HISTORY_LIMIT = 200
let msgSeq = 0
let waitTimer: ReturnType<typeof setInterval> | null = null

const chatHistory = ref<PeerChatMsg[]>([])
const waiting = ref(false)
const waitSeconds = ref(0)
const lastAttempt = ref<{ bot: string; message: string } | null>(null)
const dmListEl = ref<HTMLElement | null>(null)

// Bump to re-render the sidebar's "last message" previews after a turn ends.
const listVersion = ref(0)

// Remote bot discovery: populated when a peer is selected so the user picks a
// target from a dropdown instead of typing a name they may not know. `tag` on
// the select keeps free-text entry as a fallback (listing failed / stale).
const remoteBots = ref<PeerBotInfo[]>([])
const botsLoading = ref(false)
const botsError = ref(false)
const botOptions = computed(() =>
  remoteBots.value.map((b) => ({
    label: b.title ? `${b.name} · ${b.title}` : b.name,
    value: b.name,
  })),
)

const historyKey = computed(() =>
  dmPeer.value && dmForm.bot ? `gm.peer-dm.${dmPeer.value.name}.${dmForm.bot}` : '',
)
const canSend = computed(() => !!dmForm.bot.trim() && !!dmForm.message.trim() && !waiting.value)

// Manual "custom bot" entry: keeps the free-text fallback (remote listing
// failed / bot mode off / user knows an unadvertised bot name).
const showCustomBot = ref(false)
const customBot = ref('')

function applyCustomBot() {
  const name = customBot.value.trim()
  if (!name) return
  dmForm.bot = name
  customBot.value = ''
  showCustomBot.value = false
}

function botInitial(name: string): string {
  return (name.trim()[0] || '?').toUpperCase()
}

// Switching bots swaps to that bot's own conversation; remember the last
// targeted bot per peer so returning to a peer restores its context.
watch(() => dmForm.bot, (bot) => {
  loadHistory()
  if (dmPeer.value && bot) {
    try {
      localStorage.setItem(`gm.peer-dm.lastbot.${dmPeer.value.name}`, bot)
    } catch {
      // localStorage unavailable: history simply won't persist.
    }
  }
})

// Mobile detection (project-wide 768px breakpoint): on phones the page starts
// on the contact list — auto-selecting a peer would skip straight past it.
const isMobile = ref(window.innerWidth <= 768)

function onResize() {
  isMobile.value = window.innerWidth <= 768
  // Rotating/resizing to desktop with nothing selected: land on the first
  // peer's chat like a fresh desktop load. Moving to mobile keeps the
  // current state — the user can back out to the list themselves.
  if (!isMobile.value && !selectedPeer.value && peers.value.length) {
    selectPeer(peers.value[0])
  }
}

async function load() {
  loading.value = true
  try {
    const data = await getPeers()
    overview.value = { instance_id: data.instance_id, magic_home: data.magic_home }
    peers.value = data.peers || []
    // Refresh the selected peer's info; drop the selection if it was deleted
    // elsewhere. Nothing selected yet: land on the first peer's chat.
    if (selectedPeer.value) {
      const current = peers.value.find((p) => p.name === selectedPeer.value)
      if (current) dmPeer.value = current
      else {
        selectedPeer.value = null
        dmPeer.value = null
      }
    } else if (peers.value.length && !isMobile.value) {
      // Desktop only: phones must land on the contact list first.
      selectPeer(peers.value[0])
    }
  } catch (e) {
    message.error(e instanceof Error ? e.message : t('common.error'))
  } finally {
    loading.value = false
  }
}

// ===== Peer selection =====

// Mobile: leave the conversation and go back to the contact list.
function leaveChat() {
  selectedPeer.value = null
  dmPeer.value = null
}

function selectPeer(p: PeerInfo) {
  if (selectedPeer.value === p.name) return
  selectedPeer.value = p.name
  dmPeer.value = p
  dmForm.message = ''
  chatHistory.value = []
  lastAttempt.value = null
  remoteBots.value = []
  botsError.value = false
  showCustomBot.value = false
  customBot.value = ''
  // Reset to no-bot first so the watcher always re-runs, then restore the bot
  // this peer was last talking to (if any) — same-name bots across peers would
  // otherwise skip the watcher and keep the previous peer's history.
  dmForm.bot = ''
  try {
    const lastBot = localStorage.getItem(`gm.peer-dm.lastbot.${p.name}`) || ''
    if (lastBot) dmForm.bot = lastBot
  } catch {
    // localStorage unavailable: start with an empty picker.
  }
  loadPeerBots()
  nextTick(scrollToBottom)
}

function openAdd() {
  form.name = ''
  form.base_url = ''
  form.token = ''
  showAdd.value = true
}

async function savePeer() {
  if (!form.name.trim() || !form.base_url.trim()) {
    message.warning(t('peers.fillRequired'))
    return
  }
  saving.value = true
  try {
    const created = await addPeer({
      name: form.name.trim(),
      base_url: form.base_url.trim(),
      token: form.token.trim(),
    })
    message.success(t('peers.created'))
    showAdd.value = false
    await load()
    // Jump straight into the new peer's chat.
    const added = peers.value.find((p) => p.name === (created?.name || form.name.trim()))
    if (added) selectPeer(added)
  } catch (e) {
    message.error(e instanceof Error ? e.message : t('common.error'))
  } finally {
    saving.value = false
  }
}

async function removePeer(p: PeerInfo) {
  try {
    await deletePeer(p.name)
    message.success(t('peers.deleted'))
    if (selectedPeer.value === p.name) {
      selectedPeer.value = null
      dmPeer.value = null
    }
    await load()
  } catch (e) {
    message.error(e instanceof Error ? e.message : t('common.error'))
  }
}

// Fetch the peer's advertised bots. A failure is not fatal: the select stays
// usable as a free-text field (tag mode) and the error hint shows a retry.
async function loadPeerBots() {
  if (!dmPeer.value) return
  botsLoading.value = true
  botsError.value = false
  try {
    const res = await listPeerBots(dmPeer.value.name)
    remoteBots.value = res.bots || []
    // Only one bot advertised: preselect it instead of making the user click.
    if (!dmForm.bot && remoteBots.value.length === 1) dmForm.bot = remoteBots.value[0].name
  } catch {
    botsError.value = true
  } finally {
    botsLoading.value = false
  }
}

// ===== Conversation history (localStorage, per peer + bot) =====

function loadHistory() {
  chatHistory.value = []
  if (!historyKey.value) return
  try {
    const raw = localStorage.getItem(historyKey.value)
    if (raw) chatHistory.value = JSON.parse(raw) as PeerChatMsg[]
  } catch {
    // Corrupted history: start fresh rather than blocking the chat.
    chatHistory.value = []
  }
  nextTick(scrollToBottom)
}

function persistHistory(key: string) {
  if (!key) return
  try {
    localStorage.setItem(key, JSON.stringify(chatHistory.value.slice(-HISTORY_LIMIT)))
  } catch {
    // Storage quota issues are non-fatal: the in-memory copy still works.
  }
}

function appendMsg(role: PeerChatMsg['role'], content: string, key = historyKey.value) {
  chatHistory.value.push({ id: `m${Date.now()}-${msgSeq++}`, role, content, ts: Date.now() })
  persistHistory(key)
  listVersion.value++
  nextTick(scrollToBottom)
}

function clearHistory() {
  chatHistory.value = []
  persistHistory(historyKey.value)
  listVersion.value++
  message.success(t('peers.dmCleared'))
}

// Sidebar preview: last message of this peer's most recent conversation.
interface PeerActivity {
  ts: number
  text: string
}

function lastActivity(name: string): PeerActivity | null {
  void listVersion.value // re-read whenever a turn ends / history clears
  try {
    const lastBot = localStorage.getItem(`gm.peer-dm.lastbot.${name}`)
    if (!lastBot) return null
    const raw = localStorage.getItem(`gm.peer-dm.${name}.${lastBot}`)
    if (!raw) return null
    const msgs = JSON.parse(raw) as PeerChatMsg[]
    const last = msgs[msgs.length - 1]
    if (!last) return null
    const prefix = last.role === 'error' ? `${t('peers.dmFailed')}: ` : ''
    return { ts: last.ts, text: prefix + last.content.replace(/\s+/g, ' ').slice(0, 60) }
  } catch {
    return null
  }
}

function previewOf(p: PeerInfo): string {
  const a = lastActivity(p.name)
  return a ? a.text : t('peers.dmIdle')
}

function lastTimeOf(p: PeerInfo): string {
  const a = lastActivity(p.name)
  return a ? fmtTime(a.ts) : ''
}

// ===== Sending / receiving =====

function scrollToBottom() {
  const el = dmListEl.value
  if (el) el.scrollTop = el.scrollHeight
}

function onComposerKeydown(e: KeyboardEvent) {
  // Enter sends; Shift+Enter inserts a newline. isComposing guards against
  // the Enter keypress that commits an IME (Chinese input) composition.
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    sendDM()
  }
}

async function sendDM(explicitBot?: string, explicitContent?: string) {
  if (!dmPeer.value || waiting.value) return
  const bot = (explicitBot ?? dmForm.bot).trim()
  const content = (explicitContent ?? dmForm.message).trim()
  if (!bot || !content) {
    message.warning(t('peers.fillDm'))
    return
  }
  // Capture the conversation key up front: even if the user switches bots or
  // peers mid-turn, the reply must land in the right conversation.
  const key = `gm.peer-dm.${dmPeer.value.name}.${bot}`
  const peerName = dmPeer.value.name
  if (!explicitContent) dmForm.message = ''
  lastAttempt.value = { bot, message: content }
  appendMsg('user', content, key)
  waiting.value = true
  waitSeconds.value = 0
  const startedAt = Date.now()
  waitTimer = setInterval(() => {
    waitSeconds.value = Math.floor((Date.now() - startedAt) / 1000)
  }, 1000)
  try {
    const res = await sendPeerDM(peerName, bot, content)
    appendMsg('bot', res.reply || '', key)
  } catch (e) {
    // A relayed DM can legitimately take minutes; surface the remote error
    // verbatim as a retryable bubble rather than a generic toast.
    appendMsg('error', e instanceof Error ? e.message : t('common.error'), key)
  } finally {
    waiting.value = false
    if (waitTimer) {
      clearInterval(waitTimer)
      waitTimer = null
    }
  }
}

function retryLast() {
  if (!lastAttempt.value || waiting.value) return
  if (dmForm.bot !== lastAttempt.value.bot) dmForm.bot = lastAttempt.value.bot
  sendDM(lastAttempt.value.bot, lastAttempt.value.message)
}

function fmtTime(ts: number): string {
  return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

marked.use({ breaks: true, gfm: true })
function renderMd(content: string): string {
  if (!content) return ''
  try {
    return marked.parse(content) as string
  } catch {
    return ''
  }
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    message.success(t('peers.copied'))
  } catch {
    message.error(t('common.error'))
  }
}

onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  if (waitTimer) clearInterval(waitTimer)
})

onMounted(() => {
  window.addEventListener('resize', onResize)
  load()
})
</script>

<style scoped>
/* Hint text sitting right after a control: give it breathing room so it
   doesn't visually touch the input / descriptions above it. */
.peer-hint {
  display: block;
  margin-top: 8px;
}

/* ===== Two-pane layout: contact list + conversation =====
   Content area pads 24px on all sides with no top header, so the page fills
   exactly the remaining viewport height. */
.peers-page {
  display: flex;
  gap: 16px;
  height: calc(100vh - 48px);
  min-height: 480px;
}

/* ----- Sidebar (contact list) ----- */
.peers-sidebar {
  width: 320px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  background: #fff;
  border: 1px solid #efeff5;
  border-radius: 8px;
  overflow: hidden;
}

.peers-sidebar-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
}

.peers-title {
  margin: 0;
  font-size: 18px;
  line-height: 1.4;
}

.peers-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.peer-item {
  border: 1px solid #efeff5;
  border-radius: 8px;
  padding: 10px 12px;
  cursor: pointer;
  transition: border-color 0.2s, background-color 0.2s;
}

.peer-item:hover {
  border-color: rgba(24, 160, 88, 0.4);
}

.peer-item.active {
  border-color: #18a058;
  background: rgba(24, 160, 88, 0.06);
}

.peer-item-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.peer-item-name {
  font-size: 14px;
  font-weight: 600;
}

.peer-item-preview {
  margin-top: 4px;
  font-size: 12px;
  color: #888;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.peer-item-bottom {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 4px;
}

.peer-item-time {
  flex-shrink: 0;
  font-size: 11px;
  color: #bbb;
}

/* ----- Conversation pane ----- */
.peers-chat {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  background: #fff;
  border: 1px solid #efeff5;
  border-radius: 8px;
  overflow: hidden;
}

.peers-chat-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding: 12px 16px;
  border-bottom: 1px solid #efeff5;
}

.peers-chat-title {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.peers-chat-head-left {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  flex: 1;
}

/* Back to the contact list — mobile only, shown via media query. */
.peers-chat-back {
  display: none;
}

.peers-chat-name {
  font-size: 16px;
  font-weight: 600;
}

.peers-chat-tools {
  display: flex;
  align-items: center;
  gap: 8px;
}

/* ----- Bot chips (contact picker for the conversation pane) ----- */
.peers-chat-bots {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 10px 16px;
  border-bottom: 1px solid #efeff5;
}

.peer-bot-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 4px 12px 4px 4px;
  border: 1px solid #efeff5;
  border-radius: 999px;
  background: #fafafc;
  font-size: 13px;
  color: inherit;
  cursor: pointer;
  transition: border-color 0.2s, background-color 0.2s, color 0.2s;
}

.peer-bot-chip:hover {
  border-color: rgba(24, 160, 88, 0.4);
}

.peer-bot-chip.active {
  border-color: #18a058;
  background: rgba(24, 160, 88, 0.08);
  color: #18a058;
  font-weight: 600;
}

.peer-bot-avatar {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: #e6f5ee;
  color: #18a058;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}

.peer-bot-chip.active .peer-bot-avatar {
  background: #18a058;
  color: #fff;
}

.peer-bot-label {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.peer-bot-chip.add {
  padding: 4px 12px;
  border-style: dashed;
  color: #888;
}

.peer-bot-custom {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 240px;
}

.peers-chat-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
}

.peers-chat-empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
}

.peers-chat-composer {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 16px;
  border-top: 1px solid #efeff5;
}

.peers-composer-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

/* ===== Message bubbles ===== */
.peer-bubble-row {
  display: flex;
  gap: 8px;
  align-items: flex-end;
}

.peer-bubble-row.user {
  justify-content: flex-end;
}

.peer-bubble {
  max-width: 72%;
  padding: 8px 12px;
  border-radius: 10px;
  font-size: 13px;
  line-height: 1.6;
  word-break: break-word;
}

.peer-bubble.user {
  background: #18a058;
  color: #fff;
  white-space: pre-wrap;
  border-bottom-right-radius: 2px;
}

.peer-bubble.bot {
  background: #f4f5f7;
  border-bottom-left-radius: 2px;
}

.peer-bubble.error {
  background: #fdf3f3;
  border: 1px solid #f0c8c8;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
}

.peer-bubble-meta {
  font-size: 11px;
  color: #999;
  flex-shrink: 0;
  padding-bottom: 2px;
}

.peer-bubble-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 4px;
  font-size: 11px;
  color: #999;
}

.peer-err-title {
  font-weight: 600;
}

.peer-err-body {
  color: #b5533c;
  font-size: 12px;
  white-space: pre-wrap;
}

/* Waiting indicator: three pulsing dots + elapsed time. */
.peer-bubble.typing {
  display: flex;
  align-items: center;
  gap: 4px;
}

.peer-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #bbb;
  display: inline-block;
  animation: peer-dot-blink 1.2s infinite;
}

.peer-dot:nth-child(2) {
  animation-delay: 0.2s;
}

.peer-dot:nth-child(3) {
  animation-delay: 0.4s;
}

@keyframes peer-dot-blink {
  0%, 80%, 100% { opacity: 0.3; }
  40% { opacity: 1; }
}

.peer-wait {
  margin-left: 6px;
  font-size: 12px;
}

/* Markdown rendering inside bot bubbles. */
.peer-md :deep(p) {
  margin: 0 0 8px;
}

.peer-md :deep(p:last-child) {
  margin-bottom: 0;
}

.peer-md :deep(pre) {
  background: #1e1e1e;
  color: #e8e8e8;
  padding: 10px;
  border-radius: 6px;
  overflow-x: auto;
  font-size: 12px;
  margin: 6px 0;
}

.peer-md :deep(code) {
  font-size: 12px;
}

.peer-md :deep(ul),
.peer-md :deep(ol) {
  margin: 6px 0;
  padding-left: 20px;
}

.peer-md :deep(table) {
  border-collapse: collapse;
  margin: 6px 0;
  font-size: 12px;
}

.peer-md :deep(th),
.peer-md :deep(td) {
  border: 1px solid #ddd;
  padding: 4px 8px;
}

/* ===== Mobile (<=768px, project-wide breakpoint) =====
   Contact list and conversation each take the full width; selecting a peer
   slides into the conversation, the header back button returns to the list. */
@media (max-width: 768px) {
  .peers-page {
    gap: 12px;
    /* dvh keeps the layout stable under mobile browser URL bars. */
    height: calc(100vh - 48px);
    height: calc(100dvh - 48px);
  }

  .peers-sidebar {
    width: 100%;
    padding: 12px;
  }

  .peers-page.in-chat .peers-sidebar {
    display: none;
  }

  .peers-page:not(.in-chat) .peers-chat {
    display: none;
  }

  .peers-chat-back {
    display: inline-flex;
    flex-shrink: 0;
  }

  .peers-chat-head {
    padding: 10px 12px;
  }

  .peers-chat-bots {
    padding: 8px 12px;
  }

  .peers-chat-list {
    padding: 12px;
  }

  .peers-chat-composer {
    padding: 10px 12px;
  }

  .peer-bubble {
    max-width: 84%;
  }

  /* Keyboard hints are meaningless on touch screens. */
  .peers-hint-text {
    display: none;
  }
}
</style>
