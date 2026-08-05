/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./internal/app/api/**/*.{html,js}"],
  darkMode: 'class',
  theme: {
    extend: {
        colors: {
            'twitch-dark': '#9146FF',
            'twitch-light': '#A970FF',
            // OBS UI mockup palette for the setup guides; -d variants are the
            // dark-theme counterparts used behind dark: prefixes.
            obs: {
                'text': '#0a0a0a',
                'muted': '#646464',
                'muted-d': '#b4b4b4',
                'border': '#8c8c8c',
                'border-d': '#3c404d',
                'window': '#d3d3d3',
                'window-d': '#1d1f26',
                'chrome': '#e5e5e5',
                'chrome-d': '#272a33',
                'panel': '#ececec',
                'panel-d': '#323540',
                'canvas': '#c1c1c1',
                'canvas-d': '#13141a',
                'select': '#8cb5ff',
                'select-d': '#284cb8',
                'input-d': '#3c404d',
                'input-border-d': '#4e5566',
                'accent': '#6594eb',
                'accent-d': '#718cdc',
                'popup': '#f5f5f5',
                'black': '#111111',
                'black-d': '#0a0b0f',
                'welcome': '#0d1738',
            },
        },
        boxShadow: {
            'obs-glow': '0 0 0 3px rgba(145, 70, 255, 0.18)',
        },
    },
  },
  plugins: [],
}
