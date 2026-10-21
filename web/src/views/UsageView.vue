<template>
  <div>
    <n-space justify="space-between" style="margin-bottom: 24px;" align="center">
      <h2>{{ t('usage.title') }}</h2>
      <n-space>
        <n-select
          v-model:value="selectedDays"
          :options="daysOptions"
          style="width: 150px"
          @update:value="loadStats"
        />
        <n-button @click="loadStats">
          <template #icon>
            <n-icon><RefreshCircleOutline /></n-icon>
          </template>
          {{ t('usage.refresh') }}
        </n-button>
      </n-space>
    </n-space>

    <!-- 今日统计 -->
    <n-grid :cols="3" :x-gap="16" :y-gap="16" class="stats-grid">
      <n-gi>
        <n-card :title="t('usage.todaySessions')" size="small">
          <n-statistic :value="todayStats.sessions || 0">
            <template #suffix>{{ t('usage.sessions') }}</template>
          </n-statistic>
        </n-card>
      </n-gi>
      <n-gi>
        <n-card :title="t('usage.todayMessages')" size="small">
          <n-statistic :value="todayStats.messages || 0">
            <template #suffix>{{ t('usage.messages') }}</template>
          </n-statistic>
        </n-card>
      </n-gi>
      <n-gi>
        <n-card :title="t('usage.todayTokens')" size="small">
          <n-statistic :value="formatNumber(todayStats.total_tokens || 0)">
            <template #suffix>Tokens</template>
          </n-statistic>
        </n-card>
      </n-gi>
    </n-grid>

    <!-- 概览统计 -->
    <n-grid :cols="3" :x-gap="16" :y-gap="16" class="stats-grid" style="margin-top: 16px">
      <n-gi>
        <n-card :title="t('usage.totalSessions')" size="small">
          <n-statistic :value="insights.total_sessions || 0">
            <template #suffix>{{ t('usage.sessions') }}</template>
          </n-statistic>
        </n-card>
      </n-gi>
      <n-gi>
        <n-card :title="t('usage.totalMessages')" size="small">
          <n-statistic :value="formatNumber(insights.total_messages || 0)">
            <template #suffix>{{ t('usage.messages') }}</template>
          </n-statistic>
        </n-card>
      </n-gi>
      <n-gi>
        <n-card :title="t('usage.totalTokens')" size="small">
          <n-statistic :value="formatNumber(safeTotalTokens)">
            <template #suffix>Tokens</template>
          </n-statistic>
        </n-card>
      </n-gi>
    </n-grid>

    <!-- 详细洞察 -->
    <n-grid :cols="2" :x-gap="16" :y-gap="16" class="stats-grid" style="margin-top: 16px">
      <n-gi>
        <n-card :title="t('usage.avgTokensPerMessage')" size="small">
          <n-statistic :value="Math.round(insights.avg_tokens_per_message || 0)">
          </n-statistic>
        </n-card>
      </n-gi>
      <n-gi>
        <n-card :title="t('usage.mostUsedModel')" size="small">
          <n-statistic :value="insights.most_used_model || '-'">
          </n-statistic>
        </n-card>
      </n-gi>
    </n-grid>

    <!-- 模型用量表格 -->
    <n-card :title="t('usage.modelUsage')" style="margin-top: 16px">
      <n-data-table
        :columns="modelColumns"
        :data="insights.top_models || []"
        :pagination="false"
        :bordered="false"
      />
    </n-card>

    <!-- 每日趋势图表 -->
    <n-card :title="t('usage.dailyTrend')" style="margin-top: 16px">
      <n-data-table
        :columns="dailyColumns"
        :data="dailyData"
        :pagination="{ pageSize: 10 }"
        :bordered="false"
      />
    </n-card>

    <!-- 月度统计表格 -->
    <n-card :title="t('usage.monthlyUsage')" style="margin-top: 16px">
      <n-data-table
        :columns="monthlyColumns"
        :data="monthlyData"
        :pagination="false"
        :bordered="false"
      />
    </n-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NGrid, NGi, NCard, NStatistic, NDataTable, NSelect,
  NButton, NIcon
} from 'naive-ui'
import { NSpace } from 'naive-ui'
import { RefreshCircleOutline } from '@vicons/ionicons5'
import type { DataTableColumns } from 'naive-ui'
import {
  getUsageToday,
  getUsageDaily,
  getUsageMonthly,
  getUsageInsights,
  type TodayStats,
  type DailyUsage,
  type MonthlyUsage,
  type UsageInsight
} from '@/api/usage'

const { t } = useI18n()

const todayStats = ref<TodayStats>({
  sessions: 0,
  messages: 0,
  input_tokens: 0,
  output_tokens: 0,
  total_tokens: 0,
  avg_response_time: 0,
  top_models: []
})

const insights = ref<UsageInsight>({
  total_sessions: 0,
  total_messages: 0,
  total_input_tokens: 0,
  total_output_tokens: 0,
  avg_tokens_per_message: 0,
  most_used_model: '',
  most_active_hour: 0,
  most_active_day: ''
})

const monthlyData = ref<MonthlyUsage[]>([])
const dailyData = ref<DailyUsage[]>([])
const selectedDays = ref(30)

const daysOptions = [
  { label: '7 ' + t('usage.days'), value: 7 },
  { label: '14 ' + t('usage.days'), value: 14 },
  { label: '30 ' + t('usage.days'), value: 30 },
  { label: '90 ' + t('usage.days'), value: 90 }
]

// Safe computed for total tokens (fixes NaN)
const safeTotalTokens = computed(() => {
  const input = insights.value.total_input_tokens || 0
  const output = insights.value.total_output_tokens || 0
  const total = input + output
  return isNaN(total) ? 0 : total
})

const monthlyColumns = computed<DataTableColumns<MonthlyUsage>>(() => [
  { title: t('usage.month'), key: 'month' },
  { title: t('usage.sessions'), key: 'total_sessions' },
  { title: t('usage.messages'), key: 'total_messages' },
  {
    title: t('usage.totalTokens'),
    key: 'total_tokens',
    render: (row) => formatNumber(row.total_tokens ?? 0)
  }
])

const dailyColumns = computed<DataTableColumns<DailyUsage>>(() => [
  { title: t('usage.date'), key: 'date' },
  { title: t('usage.sessions'), key: 'sessions' },
  { title: t('usage.messages'), key: 'messages' },
  {
    title: t('usage.inputTokens'),
    key: 'input_tokens',
    render: (row) => formatNumber(row.input_tokens ?? 0)
  },
  {
    title: t('usage.outputTokens'),
    key: 'output_tokens',
    render: (row) => formatNumber(row.output_tokens ?? 0)
  },
  {
    title: t('usage.totalTokens'),
    key: 'total_tokens',
    render: (row) => formatNumber(row.total_tokens ?? 0)
  }
])

const modelColumns = computed<DataTableColumns<{
  model: string
  requests: number
  tokens: number
  percentage: number
}>>(() => [
  { title: t('usage.model'), key: 'model' },
  { title: t('usage.requests'), key: 'requests' },
  {
    title: t('usage.tokens'),
    key: 'tokens',
    render: (row) => formatNumber(row.tokens)
  },
  {
    title: t('usage.percentage'),
    key: 'percentage',
    render: (row) => `${row.percentage.toFixed(1)}%`
  }
])

function formatNumber(num: number): string {
  if (num === undefined || num === null || isNaN(num)) return '0'
  if (num >= 1000000) {
    return (num / 1000000).toFixed(2) + 'M'
  }
  if (num >= 1000) {
    return (num / 1000).toFixed(2) + 'K'
  }
  return Math.round(num).toLocaleString()
}

async function loadStats() {
  try {
    const [today, daily, monthly, insight] = await Promise.all([
      getUsageToday(),
      getUsageDaily(selectedDays.value),
      getUsageMonthly(),
      getUsageInsights()
    ])
    todayStats.value = today
    dailyData.value = daily
    monthlyData.value = monthly
    insights.value = insight
  } catch (error) {
    console.error('Failed to load usage stats:', error)
  }
}

onMounted(() => {
  loadStats()
})
</script>

<style scoped>
.stats-grid {
  margin-bottom: 24px;
}
</style>
