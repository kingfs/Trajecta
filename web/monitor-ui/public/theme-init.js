(function () {
  var stored = null;
  try {
    stored = window.localStorage.getItem("trajecta.monitor.theme");
  } catch (error) {
    /* private mode: fall through to the system preference */
  }
  var theme =
    stored === "dark" || stored === "light"
      ? stored
      : window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
  document.documentElement.dataset.theme = theme;
  // Kept in step with applyTheme(): the Radix colour scales are selected by this
  // class, and setting it here stops the first paint from showing the light
  // palette to a dark-theme visitor.
  document.documentElement.classList.toggle("dark", theme === "dark");
})();