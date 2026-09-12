/** @type {import('tailwindcss').Config} */
module.exports = {
  // .go is scanned too: history_view.go emits the filter highlight markup, and
  // a class only written there is otherwise never compiled.
  content: ["./internal/app/api/**/*.{html,js,go}"],
  darkMode: 'class',
  theme: {
    extend: {
        keyframes: {
            sheen: {
                '0%, 60%': { backgroundPosition: '200% 0' },
                '100%': { backgroundPosition: '-100% 0' },
            },
        },
        animation: {
            sheen: 'sheen 4s linear infinite',
        },
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
            // Twitch creator-dashboard mockup palette for the bits guide; -d
            // variants are the dark-theme counterparts used behind dark: prefixes.
            ttv: {
                'text': '#0e0e10',
                'muted': '#53535f',
                'muted-d': '#adadb8',
                'border': '#dedee3',
                'border-d': '#2f2f35',
                'page': '#f7f7f8',
                'page-d': '#0e0e10',
                'card': '#ffffff',
                'card-d': '#18181b',
                'row': '#f0f0f2',
                'row-d': '#1f1f23',
                'input': '#ffffff',
                'input-d': '#000000',
                'input-border': '#adadb8',
                'input-border-d': '#3f3f45',
                'btn': '#e6e6ea',
                'btn-d': '#2f2f35',
                'off': '#adadb8',
                'off-d': '#3f3f45',
                'warn': '#c28a00',
                'warn-d': '#f5c518',
                'bits': '#1fd1a5',
            },
        },
        boxShadow: {
            'obs-glow': '0 0 0 3px rgba(145, 70, 255, 0.18)',
        },
    },
  },
  plugins: [],
}
