import { createI18n } from 'vue-i18n'
import en from './en'
import zh from './zh'

function getDefaultLocale(): 'zh' | 'en' {
  // 优先级：用户显式选择（语言开关落 localStorage）> 浏览器语言 > 英语兜底。
  // 保留"跟随浏览器语言"：中文浏览器首访直接进中文，符合直觉。
  // 英语是产品的兜底默认——浏览器语言既非中文、也识别不出时一律 en。
  const saved = localStorage.getItem('locale')
  if (saved === 'zh' || saved === 'en') return saved
  const nav = typeof navigator !== 'undefined' ? navigator : undefined
  const langs = nav?.languages && nav.languages.length ? nav.languages : [nav?.language || '']
  for (const l of langs) {
    const tag = (l || '').toLowerCase()
    if (tag.startsWith('zh')) return 'zh'
    if (tag.startsWith('en')) return 'en'
  }
  return 'en'
}

const savedLocale = getDefaultLocale()

// Reflect the resolved locale on <html lang> from the very first paint, so
// assistive tech and `:lang()` styles match what is actually rendered.
if (typeof document !== 'undefined') {
  document.documentElement.setAttribute('lang', savedLocale)
}

export const i18n = createI18n({
  legacy: false,
  locale: savedLocale,
  fallbackLocale: 'en',
  messages: {
    en,
    zh
  }
})

export const locales = [
  { code: 'en', name: 'English' },
  { code: 'zh', name: '中文' }
]

export function setLocale(locale: string) {
  i18n.global.locale.value = locale as 'en' | 'zh'
  localStorage.setItem('locale', locale)
  document.documentElement.setAttribute('lang', locale)
}

export function getLocale(): string {
  return i18n.global.locale.value
}
