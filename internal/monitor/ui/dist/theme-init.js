// Set the theme before the stylesheet paints while remaining compatible with a
// strict same-origin Content-Security-Policy. Keep this in sync with applyTheme
// in src/lib/theme.js.
(function () {
  var stored = null;
  try {
    stored = window.localStorage.getItem("trajecta.monitor.theme");
  } catch (error) {
    // Private mode: fall through to the system preference.
  }
  var theme =
    stored === "dark" || stored === "light"
      ? stored
      : window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
  document.documentElement.dataset.theme = theme;
  document.documentElement.classList.toggle("dark", theme === "dark");
})();
