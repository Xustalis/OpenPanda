import type { Config } from "tailwindcss";

const config: Config = {
  content: ["./app/**/*.{ts,tsx}", "./components/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        paper: {
          DEFAULT: "#F7F4EE",
          raised: "#FCFAF5",
          deep: "#F1ECE2",
        },
        ink: {
          DEFAULT: "#1F1E1C",
          strong: "#141413",
          muted: "#6B6255",
          faint: "#756B5D",
        },
        line: {
          DEFAULT: "#E3DCCF",
          strong: "#D6CCBB",
        },
        accent: {
          DEFAULT: "#C15F3C",
          bright: "#D97757",
          deep: "#9E4A2D",
        },
      },
      fontFamily: {
        sans: [
          "-apple-system",
          "BlinkMacSystemFont",
          "Segoe UI",
          "PingFang SC",
          "Hiragino Sans GB",
          "Microsoft YaHei",
          "Noto Sans SC",
          "sans-serif",
        ],
        serif: [
          "Georgia",
          "Times New Roman",
          "Songti SC",
          "SimSun",
          "Noto Serif SC",
          "Source Han Serif SC",
          "serif",
        ],
        mono: [
          "ui-monospace",
          "SFMono-Regular",
          "SF Mono",
          "Menlo",
          "Consolas",
          "Liberation Mono",
          "monospace",
        ],
      },
      maxWidth: {
        shell: "1180px",
      },
    },
  },
  plugins: [],
};

export default config;
