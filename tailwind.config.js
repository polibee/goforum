/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    "./web/templates/**/*.html",
    "./web/js/**/*.js"
  ],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        brand: {
          50: "var(--gf-brand-50)",
          100: "var(--gf-brand-100)",
          200: "var(--gf-brand-200)",
          300: "var(--gf-brand-300)",
          400: "var(--gf-brand-400)",
          500: "var(--gf-brand-500)",
          600: "var(--gf-brand-600)",
          700: "var(--gf-brand-700)",
          800: "var(--gf-brand-800)",
          900: "var(--gf-brand-900)",
          950: "var(--gf-brand-950)"
        },
        surface: "var(--gf-surface)",
        "surface-muted": "var(--gf-surface-muted)",
        border: "var(--gf-border)",
        fg: "var(--gf-fg)",
        "fg-muted": "var(--gf-fg-muted)",
        success: "#16a34a",
        warning: "#d97706",
        danger: "#dc2626"
      },
      fontFamily: {
        sans: [
          "system-ui",
          "-apple-system",
          "Segoe UI",
          "PingFang SC",
          "Microsoft YaHei",
          "sans-serif"
        ]
      }
    }
  },
  plugins: [require("@tailwindcss/typography")]
};
