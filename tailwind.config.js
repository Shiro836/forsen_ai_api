/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./internal/app/api/**/*.{html,js}"],
  darkMode: 'class',
  theme: {
    extend: {
        colors: {
            'twitch-dark': '#9146FF',
            'twitch-light': '#A970FF',
        },
    },
  },
  plugins: [],
}
