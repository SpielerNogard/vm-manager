import { ref, watch } from 'vue'

export type ThemePreference = 'system' | 'light' | 'dark'

export const THEME_PREFERENCES: ThemePreference[] = ['system', 'light', 'dark']
// Same key and theme names as the pre-paint script in index.html.
const STORAGE_KEY = 'vmm-theme'
const THEMES = { light: 'plinth', dark: 'plinth-dark' } as const

function stored(): ThemePreference {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    return THEME_PREFERENCES.includes(v as ThemePreference) ? (v as ThemePreference) : 'system'
  } catch {
    return 'system'
  }
}

const theme = ref<ThemePreference>(stored())

// "system" drops data-theme so daisyUI's prefersdark picks the theme.
watch(
  theme,
  (t) => {
    if (t === 'system') delete document.documentElement.dataset.theme
    else document.documentElement.dataset.theme = THEMES[t]
    try {
      localStorage.setItem(STORAGE_KEY, t)
    } catch {
      // Storage denied: the choice still applies to this tab.
    }
  },
  { immediate: true },
)

export function useTheme() {
  return { theme }
}
