import { watch } from 'vue'
import { createI18n } from 'vue-i18n'

import de from './locales/de.json'
import en from './locales/en.json'

export type MessageSchema = typeof en

// To add a language: add locales/<code>.json, import it below and add an entry here.
export const LOCALES = [
  { code: 'en', name: 'English', flag: '🇬🇧' },
  { code: 'de', name: 'Deutsch', flag: '🇩🇪' },
] as const

export type Locale = (typeof LOCALES)[number]['code']

const STORAGE_KEY = 'vmm-locale'

function stored(): Locale {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    return LOCALES.some((l) => l.code === v) ? (v as Locale) : 'en'
  } catch {
    return 'en'
  }
}

export const i18n = createI18n<[MessageSchema], Locale, false>({
  legacy: false,
  locale: stored(),
  fallbackLocale: 'en',
  messages: { en, de },
})

watch(
  i18n.global.locale,
  (l) => {
    document.documentElement.lang = l
    try {
      localStorage.setItem(STORAGE_KEY, l)
    } catch {
      // Storage denied: the choice still applies to this tab.
    }
  },
  { immediate: true },
)
