/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{js,jsx}'],
  theme: {
    extend: {
      colors: {
        // galaxy 深色底：不是纯黑，带一点蓝紫，发光效果才自然
        ink: {
          900: '#0B0B14',
          800: '#12121F',
          700: '#1A1A2B',
        },
        // 主渐变三色
        grape: '#8B5CF6',
        azure: '#3B82F6',
        cyan: '#22D3EE',
      },
      boxShadow: {
        // 发光：用低透明度大扩散，不用 blur
        glow: '0 0 24px rgba(139,92,246,0.35)',
        'glow-sm': '0 0 12px rgba(139,92,246,0.45)',
        'glow-cyan': '0 0 10px rgba(34,211,238,0.6)',
      },
      backgroundImage: {
        'grad-main': 'linear-gradient(135deg, #8B5CF6 0%, #3B82F6 100%)',
        'grad-text': 'linear-gradient(120deg, #A78BFA, #60A5FA, #22D3EE)',
        'grad-bar': 'linear-gradient(90deg, #8B5CF6, #3B82F6, #22D3EE)',
      },
      animation: {
        'pulse-slow': 'pulse 3s cubic-bezier(0.4, 0, 0.6, 1) infinite',
      },
    },
  },
  plugins: [],
}
