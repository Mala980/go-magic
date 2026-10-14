<template>
  <div>
    <n-space justify="space-between" style="margin-bottom: 24px;" align="center">
      <div class="mcp-page-title">
        <h2>{{ t('mcp.title') }}</h2>
        <span class="mcp-page-subtitle">{{ t('mcp.subtitle') }}</span>
      </div>
      <n-space>
        <n-button type="primary" @click="openAddModal">
          <template #icon>
            <component :is="AddOutline" />
          </template>
          {{ t('mcp.addServer') }}
        </n-button>
        <n-dropdown
          v-if="mcpStore.servers.length"
          trigger="click"
          :options="batchOptions"
          @select="handleBatchAction"
        >
          <n-button :loading="batchBusy">
            <template #icon>
              <component :is="LayersOutline" />
            </template>
            {{ t('mcp.batchOps') }}
          </n-button>
        </n-dropdown>
        <n-button @click="handleRefresh" :loading="mcpStore.loading">
          <template #icon>
            <component :is="RefreshOutline" />
          </template>
          {{ t('common.refresh') }}
        </n-button>
      </n-space>
    </n-space>

    <n-spin v-if="mcpStore.loading && !mcpStore.servers.length" />
    <template v-else>
      <!-- Overview Cards -->
      <n-grid :cols="3" :x-gap="12" style="margin-bottom: 24px;">
        <n-gi>
          <n-card hoverable class="stat-card">
            <n-statistic :label="t('mcp.totalServers')" :value="mcpStore.servers.length" />
          </n-card>
        </n-gi>
        <n-gi>
          <n-card hoverable class="stat-card">
            <n-statistic :label="t('mcp.connected')" :value="mcpStore.connectedServers.length" />
          </n-card>
        </n-gi>
        <n-gi>
          <n-card hoverable class="stat-card">
            <n-statistic :label="t('mcp.disconnected')" :value="mcpStore.disconnectedServers.length" />
          </n-card>
        </n-gi>
      </n-grid>

      <!-- 失败原因汇总提示条：有未连接且已知原因的服务器时出现 -->
      <n-alert
        v-if="failedServers.length && !failuresDismissed"
        type="warning"
        :show-icon="true"
        closable
        style="margin-bottom: 16px;"
        @close="dismissFailures"
      >
        <template #header>
          {{ t('mcp.failuresTitle', { count: failedServers.length }) }}
        </template>
        <div v-for="s in failedServers" :key="s.name" class="failure-line">
          <n-text strong>{{ s.name }}</n-text>
          <n-text depth="3" style="font-size: 12px;">{{ s.last_error }}</n-text>
        </div>
      </n-alert>

      <!-- Server List -->
      <n-card :title="t('mcp.servers')">
        <n-table
          :columns="columns"
          :data="mcpStore.servers"
          row-key="name"
          :pagination="false"
          :row-props="rowProps"
          @update:expanded-row-keys="handleRowExpand"
        >
          <template #body-cell:status="{ row }">
            <n-space :size="4" align="center">
              <n-tag :type="row.connected ? 'success' : 'error'" size="small">
                {{ row.connected ? t('mcp.statusConnected') : t('mcp.statusDisconnected') }}
              </n-tag>
              <n-tag v-if="row.configured === false" size="small" :bordered="false">
                {{ t('mcp.notConfigured') }}
              </n-tag>
            </n-space>
          </template>

          <template #body-cell:transport="{ row }">
            <n-tag :type="row.transport === 'stdio' ? 'info' : 'warning'" size="small">
              {{ (row.transport || 'stdio').toUpperCase() }}
            </n-tag>
          </template>

          <template #body-cell:tool_count="{ row }">
            <n-tag
              v-if="row.connected"
              size="small"
              :bordered="false"
              :type="row.tool_count > 0 ? 'default' : 'warning'"
            >
              {{ row.tool_count }}
            </n-tag>
            <n-text v-else depth="3">-</n-text>
          </template>

          <template #body-cell:last_health_check="{ row }">
            <n-text depth="3" style="font-size: 12px;">
              {{ row.last_health_check ? formatTime(row.last_health_check) : '-' }}
            </n-text>
          </template>

          <template #body-cell:actions="{ row }">
            <n-space :size="8">
              <n-tooltip v-if="row.configured">
                <template #trigger>
                  <n-button text size="small" @click="openEditModal(row)">
                    <template #icon>
                      <component :is="CreateOutline" />
                    </template>
                  </n-button>
                </template>
                {{ t('common.edit') }}
              </n-tooltip>
              <n-tooltip>
                <template #trigger>
                  <n-button
                    text
                    size="small"
                    @click="handleHealthCheck(row.name)"
                    :disabled="!row.connected"
                  >
                    <template #icon>
                      <component :is="HeartOutline" />
                    </template>
                  </n-button>
                </template>
                {{ t('mcp.healthCheck') }}
              </n-tooltip>
              <n-tooltip>
                <template #trigger>
                  <n-button
                    text
                    size="small"
                    @click="handleReconnect(row.name)"
                    :disabled="row.connected"
                  >
                    <template #icon>
                      <component :is="RefreshCircleOutline" />
                    </template>
                  </n-button>
                </template>
                {{ t('mcp.reconnect') }}
              </n-tooltip>
              <n-tooltip>
                <template #trigger>
                  <n-button
                    text
                    size="small"
                    @click="handleCopyConfig(row)"
                  >
                    <template #icon>
                      <component :is="CopyOutline" />
                    </template>
                  </n-button>
                </template>
                {{ t('mcp.copyConfig') }}
              </n-tooltip>
              <n-tooltip>
                <template #trigger>
                  <n-button
                    text
                    size="small"
                    type="error"
                    @click="handleDisconnect(row.name)"
                    :disabled="!row.connected"
                  >
                    <template #icon>
                      <component :is="PowerOutline" />
                    </template>
                  </n-button>
                </template>
                {{ t('mcp.disconnect') }}
              </n-tooltip>
              <n-tooltip>
                <template #trigger>
                  <n-button text size="small" @click="handleDelete(row.name)">
                    <template #icon>
                      <component :is="TrashOutline" />
                    </template>
                  </n-button>
                </template>
                {{ t('common.delete') }}
              </n-tooltip>
            </n-space>
          </template>

          <template #expanded-row="{ row }">
            <div class="expanded-row">
              <!-- 未连接：直接说明原因 + 给出重连入口 -->
              <n-alert
                v-if="!row.connected && row.last_error"
                type="error"
                :show-icon="true"
                style="margin-bottom: 12px;"
              >
                <template #header>{{ t('mcp.connectFailed') }}</template>
                <div class="error-detail">{{ row.last_error }}</div>
              </n-alert>

              <n-card size="small" :title="t('mcp.tools')">
                <template #header-extra>
                  <n-space :size="8" align="center">
                    <n-text depth="3" style="font-size: 12px;">
                      {{ t('mcp.toolCountLabel', { count: serverTools[row.name]?.length ?? 0 }) }}
                    </n-text>
                    <n-button
                      text
                      size="small"
                      @click="handleRefreshTools(row.name)"
                      :disabled="!row.connected"
                    >
                      <template #icon>
                        <component :is="RefreshOutline" />
                      </template>
                      {{ t('mcp.refreshTools') }}
                    </n-button>
                  </n-space>
                </template>

                <n-spin v-if="toolsLoading[row.name]" size="small" />
                <template v-else-if="serverTools[row.name]?.length">
                  <n-collapse>
                    <n-collapse-item
                      v-for="tool in serverTools[row.name]"
                      :key="tool.name"
                      :name="tool.name"
                    >
                      <template #header>
                        <n-space :size="8" align="center">
                          <n-text code>{{ tool.name }}</n-text>
                        </n-space>
                      </template>
                      <template #header-extra>
                        <n-button
                          text
                          size="tiny"
                          @click.stop="handleCopyToolName(row.name, tool.name)"
                        >
                          <template #icon>
                            <component :is="CopyOutline" />
                          </template>
                        </n-button>
                      </template>
                      <n-space vertical :size="8">
                        <n-text v-if="tool.description" depth="2" style="font-size: 13px;">
                          {{ tool.description }}
                        </n-text>
                        <n-text v-else depth="3" style="font-size: 12px;">
                          {{ t('mcp.noDescription') }}
                        </n-text>
                        <div v-if="hasSchema(tool)">
                          <n-text depth="3" style="font-size: 12px; display: block; margin-bottom: 4px;">
                            {{ t('mcp.parameters') }}
                          </n-text>
                          <pre class="schema-pre">{{ formatSchema(tool) }}</pre>
                        </div>
                        <n-text depth="3" style="font-size: 12px;">
                          {{ t('mcp.callAs') }}
                          <n-text code>{{ registryToolName(row.name, tool.name) }}</n-text>
                        </n-text>
                      </n-space>
                    </n-collapse-item>
                  </n-collapse>
                </template>
                <n-empty
                  v-else
                  :description="row.connected ? t('mcp.noTools') : t('mcp.connectFirst')"
                />
              </n-card>
            </div>
          </template>
        </n-table>

        <n-empty v-if="!mcpStore.servers.length" :description="t('mcp.noServers')">
          <template #extra>
            <n-button size="small" @click="openAddModal">{{ t('mcp.addServer') }}</n-button>
          </template>
        </n-empty>
      </n-card>
    </template>

    <!-- Add/Edit Server Modal -->
    <n-modal
      v-model:show="showAddModal"
      :title="isEditing ? t('mcp.editServer') : t('mcp.addServer')"
      preset="card"
      class="modal-responsive modal-scroll"
      style="width: 520px; max-width: 96vw; max-height: 85vh;"
    >
      <!-- 弹窗打开后第一个可聚焦元素是"服务名称"输入框（naive 的 focus trap 行为）。
           从别处复制一段 JSON 直接 Ctrl+V，内容会整段落进名字框——而 JSON 输入框在
           另一个分支里，用户看到的就是"粘贴没反应、内容跑到表单里了"。
           这里在捕获阶段先看一眼剪贴板：像 JSON 就拦下来，切到 JSON 模式再填入。 -->
      <div class="mcp-modal-body" @paste.capture="onModalPaste">
        <n-space vertical>
          <n-form-item v-if="!isEditing" :label="t('mcp.inputMode')">
            <n-radio-group v-model:value="inputMode" size="small">
              <n-radio-button value="form">{{ t('mcp.modeForm') }}</n-radio-button>
              <n-radio-button value="json">{{ t('mcp.modeJSON') }}</n-radio-button>
            </n-radio-group>
          </n-form-item>

          <!--
            两种录入方式各自包一层**带唯一 key** 的容器，这一步不是装饰，是修 bug 的：

            两支若都写成 <template v-if>/<template v-else>，编译器会产出两个 Fragment，
            Vue 认为"同一位置、同类节点"，于是**原地复用** DOM 与组件实例，只做 props 补丁；
            两支里的 NFormItem → NInput 恰好同类型，JSON 分支的控件就被复用成了表单分支
            那个单行 input（placeholder 还是 e.g., filesystem，rows/type 全被吃掉）。
            现象就是"切到 JSON 之后，JSON 配置下面还是个小单行框"。
            显式 key 让两支强制卸载/重建，彻底断掉复用。
          -->
          <!-- JSON 模式：粘贴别的 MCP 客户端（Claude Desktop / Cursor / Cline）
               或 config.json 里的片段即可，支持一次导入多个服务器。 -->
          <div v-if="jsonMode" key="mcp-mode-json">
            <n-form-item :label="t('mcp.jsonConfig')" required>
              <n-input
                key="mcp-json-input"
                ref="jsonInputRef"
                v-model:value="jsonText"
                type="textarea"
                :rows="12"
                :placeholder="jsonPlaceholder"
              />
            </n-form-item>
            <div class="json-preview">
              <n-text v-if="jsonPreview.error" type="error" style="font-size: 12px;">
                {{ t('mcp.jsonInvalid') }}{{ jsonPreview.error }}
              </n-text>
              <n-text v-else-if="jsonPreview.names.length" depth="3" style="font-size: 12px;">
                {{ t('mcp.jsonDetected', { count: jsonPreview.names.length }) }}
                <span class="json-names">{{ jsonPreview.names.join(', ') }}</span>
              </n-text>
              <n-text v-else depth="3" style="font-size: 12px;">
                {{ t('mcp.jsonHint') }}
              </n-text>
            </div>
          </div>

          <div v-else key="mcp-mode-form">
            <n-form-item :label="t('mcp.serverName')" required>
              <n-input
                key="mcp-name-input"
                v-model:value="formData.name"
                :disabled="isEditing"
                placeholder="e.g., filesystem"
              />
            </n-form-item>

            <n-form-item :label="t('mcp.transport')" required>
              <n-select
                key="mcp-transport-select"
                v-model:value="formData.transport"
                :options="transportOptions"
                placeholder="Select transport"
              />
            </n-form-item>

            <n-form-item :label="t('mcp.command')" v-if="formData.transport === 'stdio'" required>
              <n-input
                key="mcp-command-input"
                v-model:value="formData.command"
                placeholder="e.g., npx"
              />
            </n-form-item>

            <n-form-item :label="t('mcp.args')" v-if="formData.transport === 'stdio'">
              <n-input
                key="mcp-args-input"
                v-model:value="formData.argsStr"
                type="textarea"
                :rows="3"
                placeholder="-y @modelcontextprotocol/server-filesystem /tmp"
              />
            </n-form-item>

            <n-form-item :label="t('mcp.url')" v-if="formData.transport === 'sse'" required>
              <n-input
                key="mcp-url-input"
                v-model:value="formData.url"
                placeholder="http://localhost:8080/mcp"
              />
            </n-form-item>

            <n-form-item :label="t('mcp.env')">
              <n-input
                key="mcp-env-input"
                v-model:value="formData.envStr"
                type="textarea"
                :rows="2"
                placeholder="KEY=value&#10;ANOTHER_KEY=value"
              />
            </n-form-item>
          </div>
        </n-space>
      </div>

      <template #footer>
        <n-space justify="end">
          <n-button @click="showAddModal = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" @click="handleSave">{{ t('common.save') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, nextTick, watch } from 'vue'
import { useMessage } from 'naive-ui'
import { NAlert, NButton, NCard, NCollapse, NCollapseItem, NDropdown, NEmpty, NFormItem, NGi, NGrid, NInput, NModal, NRadioButton, NRadioGroup, NSelect, NSpace, NSpin, NStatistic, NTable, NTag, NText, NTooltip } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import {
  AddOutline,
  RefreshOutline,
  HeartOutline,
  RefreshCircleOutline,
  PowerOutline,
  TrashOutline,
  CreateOutline,
  CopyOutline,
  LayersOutline,
} from '@vicons/ionicons5'
import { useMCPStore } from '@/stores/mcp'
import { mcpErrorMessage, serverConfigToJSON } from '@/api/mcp'
import type { MCPConfig, MCPServer } from '@/api/mcp'

const { t } = useI18n()
const message = useMessage()
const mcpStore = useMCPStore()

const showAddModal = ref(false)
const isEditing = ref(false)
const serverTools = ref<Record<string, any[]>>({})
const toolsLoading = ref<Record<string, boolean>>({})
const batchBusy = ref(false)
const failuresDismissed = ref(false)

// 添加弹窗的两种录入方式：表单（单服务器）与 JSON（可一次导入多个）。
const inputMode = ref<'form' | 'json'>('form')
const jsonText = ref('')
// naive 的 n-input 实例：切到 JSON 模式后把光标放进去，用户点完"JSON"就能直接
// Ctrl+V，不会再出现"光标还在表单里"的错位粘贴。
const jsonInputRef = ref<any>(null)

// 只有"新增 + JSON 模式"才走 JSON 分支；编辑既有服务器永远走表单。
// 单独抽成 computed：模板里两处（分支条件、粘贴判断）共用一份口径，避免写岔。
const jsonMode = computed(() => inputMode.value === 'json' && !isEditing.value)

const jsonPlaceholder = `{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": { "TOKEN": "xxx" }
    }
  }
}`

const formData = reactive({
  name: '',
  transport: 'stdio',
  command: '',
  argsStr: '',
  url: '',
  envStr: '',
})

const transportOptions = [
  { label: 'STDIO', value: 'stdio' },
  { label: 'SSE', value: 'sse' },
]

/**
 * 本地预检粘贴的 JSON —— 只为在提交前给出即时反馈（列出识别到的服务器名）。
 * 真正的权威解析在后端（internal/mcp/import.go），它接受的形态更多
 * （jsonc 注释、http 传输、args 整串写法…），所以这里识别不出来时不做拦截。
 */
const jsonPreview = computed<{ names: string[]; error: string }>(() => {
  const text = jsonText.value.trim()
  if (!text) return { names: [], error: '' }

  let raw: any
  try {
    raw = JSON.parse(text)
  } catch (e: any) {
    return { names: [], error: String(e?.message ?? e) }
  }

  const names = new Set<string>()
  const collect = (node: any): boolean => {
    if (Array.isArray(node)) {
      node.forEach((item) => {
        if (item && typeof item === 'object' && typeof item.name === 'string') names.add(item.name)
      })
      return true
    }
    if (!node || typeof node !== 'object') return false
    for (const key of ['mcpServers', 'servers', 'mcp']) {
      if (node[key] && typeof node[key] === 'object') return collect(node[key])
    }
    if (typeof node.name === 'string' && (node.command || node.url || node.type || node.transport)) {
      names.add(node.name)
      return true
    }
    let sawObject = false
    Object.entries(node).forEach(([key, value]) => {
      if (value && typeof value === 'object' && !Array.isArray(value)) {
        names.add(key)
        sawObject = true
      }
    })
    return sawObject
  }

  if (!collect(raw)) return { names: [], error: '' }
  return { names: [...names], error: '' }
})

const columns = [
  {
    title: t('mcp.serverName'),
    key: 'name',
    render: (row: any) => ({
      type: 'expand',
      expandTrigger: 'row',
      children: row.name,
    }),
  },
  {
    title: t('mcp.status'),
    key: 'status',
    width: 120,
  },
  {
    title: t('mcp.transport'),
    key: 'transport',
    width: 100,
  },
  {
    title: t('mcp.toolCount'),
    key: 'tool_count',
    width: 100,
    align: 'center',
  },
  {
    title: t('mcp.lastHealthCheck'),
    key: 'last_health_check',
    width: 180,
  },
  {
    title: t('common.actions'),
    key: 'actions',
    width: 280,
  },
]

/** 未连接 + 已知失败原因的服务器（供失败汇总提示条使用）。 */
const failedServers = computed(() =>
  mcpStore.servers.filter(s => !s.connected && !!s.last_error),
)

/** 行属性：未连接的服务器整行淡色，强化"这里有状态"的视觉提示。 */
function rowProps(row: MCPServer) {
  return {
    style: row.connected ? '' : 'opacity: 0.72;',
  }
}

const batchOptions = computed(() => [
  {
    label: t('mcp.batchReconnectDisconnected'),
    key: 'reconnect',
    disabled: mcpStore.disconnectedServers.length === 0,
  },
  {
    label: t('mcp.batchHealthCheckAll'),
    key: 'health',
    disabled: mcpStore.connectedServers.length === 0,
  },
])

async function handleBatchAction(key: string) {
  if (key === 'reconnect') {
    const names = mcpStore.disconnectedServers.map(s => s.name)
    if (!names.length) return
    batchBusy.value = true
    try {
      const results = await mcpStore.reconnectMany(names)
      const ok = results.filter(r => r.ok).length
      const bad = results.filter(r => !r.ok)
      if (bad.length) {
        message.warning(
          `${t('mcp.batchReconnectResult', { ok, failed: bad.length })} ${bad.map(b => `${b.name}: ${b.error}`).join('; ')}`,
          { duration: 8000 },
        )
      } else {
        message.success(t('mcp.batchReconnectResult', { ok, failed: 0 }))
      }
    } finally {
      batchBusy.value = false
    }
    return
  }

  if (key === 'health') {
    batchBusy.value = true
    try {
      const results = await mcpStore.healthCheckAll()
      const bad = results.filter(r => !r.healthy)
      if (bad.length) {
        message.warning(
          `${t('mcp.batchHealthResult', { ok: results.length - bad.length, failed: bad.length })} ${bad.map(b => `${b.name}: ${b.error ?? ''}`).join('; ')}`,
          { duration: 8000 },
        )
      } else {
        message.success(t('mcp.batchHealthResult', { ok: results.length, failed: 0 }))
      }
    } finally {
      batchBusy.value = false
    }
  }
}

function dismissFailures() {
  failuresDismissed.value = true
}

function hasSchema(tool: any): boolean {
  const schema = tool?.inputSchema
  if (!schema || typeof schema !== 'object') return false
  const props = schema.properties
  return !!props && Object.keys(props).length > 0
}

function formatSchema(tool: any): string {
  const schema = tool?.inputSchema ?? {}
  const props = schema.properties ?? {}
  const required: string[] = Array.isArray(schema.required) ? schema.required : []
  const lines = Object.entries(props).map(([key, value]: [string, any]) => {
    const type = value?.type ?? 'any'
    const desc = value?.description ? ` — ${value.description}` : ''
    const req = required.includes(key) ? ' *' : ''
    return `  ${key}: ${type}${req}${desc}`
  })
  return lines.length ? lines.join('\n') : JSON.stringify(schema, null, 2)
}

/** 工具在 Agent 注册表里的名字：mcp_<server>_<tool>。 */
function registryToolName(serverName: string, toolName: string): string {
  return `mcp_${serverName}_${toolName}`
}

async function copyText(text: string, okMsg: string) {
  try {
    await navigator.clipboard.writeText(text)
    message.success(okMsg)
  } catch {
    message.error(t('common.copyFailed'))
  }
}

function handleCopyConfig(row: MCPServer) {
  copyText(serverConfigToJSON(row), t('mcp.configCopied'))
}

function handleCopyToolName(serverName: string, toolName: string) {
  copyText(registryToolName(serverName, toolName), t('common.copied'))
}

function formatTime(timeStr: string): string {
  if (!timeStr) return '-'
  return new Date(timeStr).toLocaleString()
}

async function handleRefresh() {
  await mcpStore.loadServers()
  message.success(t('common.refreshed'))
}

async function handleRowExpand(keys: string[]) {
  for (const key of keys) {
    if (!serverTools.value[key]) {
      toolsLoading.value[key] = true
      try {
        serverTools.value[key] = await mcpStore.loadServerTools(key)
      } finally {
        toolsLoading.value[key] = false
      }
    }
  }
}

async function handleHealthCheck(name: string) {
  try {
    await mcpStore.healthCheck(name)
    const status = mcpStore.getHealthStatus(name)
    if (status) {
      message.success(t('mcp.healthOK', { name }))
    } else {
      const detail = mcpStore.getLastError(name)
      message.error(detail ? `${t('mcp.healthFailed', { name })}: ${detail}` : t('mcp.healthFailed', { name }))
    }
    // 健康检查会改变 connected / last_error / last_health_check，刷新以同步界面
    await mcpStore.loadServers()
    if (mcpStore.servers.find(s => s.name === name)?.connected) {
      serverTools.value[name] = await mcpStore.loadServerTools(name)
    }
  } catch (e: any) {
    message.error(mcpErrorMessage(e) || t('mcp.healthFailed', { name }))
  }
}

async function handleReconnect(name: string) {
  try {
    await mcpStore.reconnectServer(name)
    message.success(t('mcp.reconnected', { name }))
    // 重连成功后工具清单可能变化，重新拉一次
    delete serverTools.value[name]
    serverTools.value[name] = await mcpStore.loadServerTools(name)
  } catch (e: any) {
    message.error(mcpErrorMessage(e) || t('mcp.reconnectFailed', { name }))
  }
}

async function handleDisconnect(name: string) {
  try {
    await mcpStore.disconnectServer(name)
    message.success(t('mcp.serverDisconnected', { name }))
    // 断开后工具不再可用，清掉缓存避免误导
    delete serverTools.value[name]
  } catch (e: any) {
    message.error(mcpErrorMessage(e) || t('mcp.disconnectFailed', { name }))
  }
}

async function handleDelete(name: string) {
  try {
    await mcpStore.removeServer(name)
    delete serverTools.value[name]
    message.success(t('mcp.deleted', { name }))
  } catch (e: any) {
    message.error(mcpErrorMessage(e) || t('mcp.deleteFailed', { name }))
  }
}

async function handleRefreshTools(name: string) {
  try {
    serverTools.value[name] = await mcpStore.refreshTools(name)
    message.success(t('mcp.toolsRefreshed'))
  } catch (e: any) {
    message.error(mcpErrorMessage(e) || t('mcp.refreshToolsFailed'))
  }
}

// 切到 JSON 模式后把光标移进 JSON 输入框。
watch(inputMode, (mode) => {
  if (mode !== 'json') return
  nextTick(() => jsonInputRef.value?.focus?.())
})

/**
 * 弹窗内的粘贴兜底。
 *
 * 为什么需要：弹窗打开后 naive 的 focus trap 会把焦点放在第一个可聚焦元素上，
 * 也就是"服务名称"输入框。此时从别处复制一段 mcpServers JSON 直接 Ctrl+V，
 * 整段 JSON 会落进名字框，而 JSON 输入框在另一个分支里是空的 —— 用户看到的
 * 就是"粘贴没反应、内容跑到表单里了"。服务器名的合法字符只有 [A-Za-z0-9_.-]，
 * 以 { 或 [ 开头的一定是配置，所以在捕获阶段拦下来换成 JSON 模式。
 *
 * 注意：从文档/README/聊天里复制时经常把 Markdown 代码围栏（```json ... ```）
 * 一起带走，那样首字符是 ` 而不是 {，只按"首字符"判断仍然会漏 —— 所以先剥围栏。
 */
function stripCodeFence(text: string): string {
  const m = text.match(/^\s*`{3,}[^\n]*\r?\n([\s\S]*?)\r?\n?\s*`{3,}\s*$/)
  return m ? m[1] : text
}

function onModalPaste(e: ClipboardEvent) {
  // 编辑模式没有 JSON 分支；已经在 JSON 模式下就交给 textarea 自己处理
  if (jsonMode.value) return

  const raw = e.clipboardData?.getData('text') ?? ''
  const text = stripCodeFence(raw)
  const trimmed = text.trim()
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return

  e.preventDefault()
  jsonText.value = text
  inputMode.value = 'json'
  message.info(t('mcp.jsonPasted'), { duration: 3000 })
}

function openAddModal() {
  isEditing.value = false
  inputMode.value = 'form'
  jsonText.value = ''
  Object.assign(formData, { name: '', transport: 'stdio', command: '', argsStr: '', url: '', envStr: '' })
  showAddModal.value = true
}

function openEditModal(row: MCPServer) {
  isEditing.value = true
  inputMode.value = 'form'
  Object.assign(formData, {
    name: row.name,
    transport: row.transport || 'stdio',
    command: row.command || '',
    argsStr: (row.args || []).join(' '),
    url: row.url || '',
    // env 的值是 *** 掩码，原样回传时由后端还原成磁盘上的真值
    envStr: Object.entries(row.env || {}).map(([k, v]) => `${k}=${v}`).join('\n'),
  })
  showAddModal.value = true
}

async function handleSave() {
  if (inputMode.value === 'json' && !isEditing.value) {
    await handleImportJSON()
    return
  }

  if (!formData.name) {
    message.error(t('mcp.serverNameRequired'))
    return
  }

  if (formData.transport === 'stdio' && !formData.command) {
    message.error(t('mcp.commandRequired'))
    return
  }

  if (formData.transport === 'sse' && !formData.url) {
    message.error(t('mcp.urlRequired'))
    return
  }

  const config: MCPConfig = {
    command: formData.command,
    args: formData.argsStr.split(/\s+/).filter(Boolean),
    transport: formData.transport,
    url: formData.url,
    env: formData.envStr.split('\n').filter(Boolean),
  }

  try {
    if (isEditing.value) {
      await mcpStore.updateServer(formData.name, config)
      message.success(t('mcp.serverUpdated'))
    } else {
      await mcpStore.addServer(formData.name, config)
      message.success(t('mcp.serverAdded'))
    }
    showAddModal.value = false
  } catch (e: any) {
    message.error(mcpErrorMessage(e) || t('mcp.saveFailed'))
  }
}

async function handleImportJSON() {
  if (!jsonText.value.trim()) {
    message.error(t('mcp.jsonRequired'))
    return
  }
  try {
    const result = await mcpStore.importServersFromJSON(jsonText.value)
    const failed = result?.failed ?? []
    if (failed.length) {
      // 部分成功：配置已经写盘，只是有几个连不上（报错内容来自后端）。
      const detail = failed.map(f => `${f.name}: ${f.error}`).join('; ')
      message.warning(
        `${t('mcp.jsonPartial', { added: result?.added?.length ?? 0, failed: failed.length })} ${detail}`,
        { duration: 8000 },
      )
    } else {
      message.success(t('mcp.jsonAdded', { count: result?.added?.length ?? 0 }))
    }
    if (result?.warning) {
      message.warning(result.warning, { duration: 8000 })
    }
    showAddModal.value = false
  } catch (e: any) {
    message.error(mcpErrorMessage(e) || t('mcp.saveFailed'))
  }
}

onMounted(async () => {
  await mcpStore.loadServers()
})
</script>

<style scoped>
/* 页面标题 + 一行副标题：MCP 协议细节收进副标题，主标题统一叫「连接器」 */
.mcp-page-title {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.mcp-page-title h2 {
  margin: 0;
}

.mcp-page-subtitle {
  font-size: 12px;
  color: #9ca3af;
}

/* Action buttons inside table cells: more compact to fit narrower columns */
.btn-condensed {
  padding: 0 6px;
}

/* JSON 模式的预检提示 */
.json-preview {
  margin-top: -12px;
  padding: 0 2px 4px;
  line-height: 1.5;
  word-break: break-all;
}

.json-names {
  color: var(--primary-color, #2080f0);
  font-family: var(--font-family-mono, monospace);
}

/* 失败汇总提示条里的一行：服务器名 + 原因 */
.failure-line {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 2px 0;
}

.failure-line + .failure-line {
  border-top: 1px dashed rgba(128, 128, 128, 0.2);
  margin-top: 4px;
  padding-top: 4px;
}

/* 展开行内的容器留白 */
.expanded-row {
  padding: 16px;
}

.error-detail {
  font-family: var(--font-family-mono, monospace);
  font-size: 12px;
  line-height: 1.6;
  word-break: break-all;
  white-space: pre-wrap;
}

/* 工具参数 schema 预览块 */
.schema-pre {
  margin: 0;
  padding: 8px 10px;
  border-radius: 4px;
  background: rgba(128, 128, 128, 0.08);
  font-family: var(--font-family-mono, monospace);
  font-size: 12px;
  line-height: 1.6;
  overflow-x: auto;
  white-space: pre;
}

/* 概览卡片在窄屏下堆叠 */
@media (max-width: 640px) {
  :deep(.n-grid) {
    grid-template-columns: 1fr !important;
  }
}

/* 移动端:表格横向滚动 + 底部安全区 */
@media (max-width: 768px) {
  :deep(.n-data-table-wrapper) {
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  }

  .expanded-row {
    padding: 12px 8px;
  }
}
</style>
