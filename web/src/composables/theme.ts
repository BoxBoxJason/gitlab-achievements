import { ref, watch } from 'vue'

export type ThemeMode = 'auto' | 'light' | 'dark'

function saved(): ThemeMode {
  try {
    const mode = localStorage.getItem('theme')
    if (mode === 'light' || mode === 'dark') return mode
  } catch {
    // Storage unavailable: follow the system.
  }

  return 'auto'
}

/** The color mode, applied to <html> and remembered in this browser only. */
export const theme = ref<ThemeMode>(saved())

watch(theme, (mode) => {
  if (mode === 'auto') delete document.documentElement.dataset.theme
  else document.documentElement.dataset.theme = mode

  try {
    if (mode === 'auto') localStorage.removeItem('theme')
    else localStorage.setItem('theme', mode)
  } catch {
    // Not remembered, but still applied for this visit.
  }
})
