// Applies the saved color mode before the first paint. It is a file of its
// own rather than an inline script because the app's Content Security
// Policy allows no inline script.
try {
  const mode = localStorage.getItem('theme')
  if (mode === 'light' || mode === 'dark') document.documentElement.dataset.theme = mode
} catch {
  // Storage can be unavailable (private windows, blocked site data); the
  // system preference applies then.
}
