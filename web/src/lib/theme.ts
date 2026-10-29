export type ThemeChoice = "light" | "dark" | "system";

const key = "assay.theme";

export function storedTheme(): ThemeChoice {
  try {
    const v = localStorage.getItem(key);
    if (v === "light" || v === "dark") return v;
  } catch {
    // Storage can be blocked. The system theme applies then.
  }
  return "system";
}

export function applyTheme(choice: ThemeChoice) {
  const dark =
    choice === "dark" || (choice === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
}

export function setTheme(choice: ThemeChoice) {
  try {
    if (choice === "system") localStorage.removeItem(key);
    else localStorage.setItem(key, choice);
  } catch {
    // Storage can be blocked. The choice then lasts for this page only.
  }
  applyTheme(choice);
}

export function watchSystemTheme() {
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (storedTheme() === "system") applyTheme("system");
  });
}
