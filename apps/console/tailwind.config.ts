import type { Config } from 'tailwindcss';
import animate from 'tailwindcss-animate';

const config: Config = {
  darkMode: ['class'],
  content: [
    './app/**/*.{ts,tsx}',
    './components/**/*.{ts,tsx}',
    './lib/**/*.{ts,tsx}',
  ],
  theme: {
    extend: {
      colors: {
        border: 'hsl(var(--border))',
        background: 'hsl(var(--background))',
        foreground: 'hsl(var(--foreground))',
        orange: {
          DEFAULT: 'hsl(var(--orange))',
          foreground: 'hsl(0 0% 100%)',
          50:  'hsl(25 95% 95%)',
          100: 'hsl(25 95% 87%)',
          200: 'hsl(25 95% 77%)',
          300: 'hsl(25 95% 65%)',
          400: 'hsl(25 95% 53%)',
          500: 'hsl(25 95% 47%)',
          600: 'hsl(25 95% 41%)',
          700: 'hsl(25 95% 35%)',
          800: 'hsl(25 95% 27%)',
          900: 'hsl(25 95% 18%)',
        },
        blue: {
          DEFAULT: 'hsl(var(--blue))',
          foreground: 'hsl(0 0% 100%)',
          50:  'hsl(210 100% 95%)',
          100: 'hsl(210 100% 87%)',
          200: 'hsl(210 100% 77%)',
          300: 'hsl(210 100% 65%)',
          400: 'hsl(210 100% 53%)',
          500: 'hsl(210 100% 47%)',
          600: 'hsl(210 100% 41%)',
          700: 'hsl(210 100% 35%)',
          800: 'hsl(210 100% 27%)',
          900: 'hsl(210 100% 18%)',
        },
        primary: {
          DEFAULT: 'hsl(var(--orange))',
          foreground: 'hsl(0 0% 100%)',
        },
        secondary: {
          DEFAULT: 'hsl(var(--blue))',
          foreground: 'hsl(0 0% 100%)',
        },
        muted: {
          DEFAULT: 'hsl(var(--muted))',
          foreground: 'hsl(var(--muted-foreground))',
        },
        accent: {
          DEFAULT: 'hsl(var(--blue))',
          foreground: 'hsl(0 0% 100%)',
        },
        card: {
          DEFAULT: 'hsl(var(--card))',
          foreground: 'hsl(var(--card-foreground))',
        },
        destructive: {
          DEFAULT: 'hsl(0 84% 60%)',
          foreground: 'hsl(0 0% 100%)',
        },
        success: {
          DEFAULT: 'hsl(142 76% 36%)',
          foreground: 'hsl(0 0% 100%)',
        },
        warning: {
          DEFAULT: 'hsl(38 92% 50%)',
          foreground: 'hsl(0 0% 100%)',
        },
      },
      borderRadius: {
        lg: '0.75rem',
        md: '0.5rem',
        sm: '0.25rem',
        xl: '1rem',
      },
      boxShadow: {
        'glow-orange': '0 0 20px hsl(25 95% 53% / 0.3)',
        'glow-blue': '0 0 20px hsl(210 100% 47% / 0.3)',
        'glow-sm-orange': '0 0 10px hsl(25 95% 53% / 0.2)',
        'glow-sm-blue': '0 0 10px hsl(210 100% 47% / 0.2)',
        'card-hover': '0 8px 30px hsl(0 0% 0% / 0.12)',
      },
      backgroundImage: {
        'gradient-brand': 'linear-gradient(135deg, hsl(25 95% 53%) 0%, hsl(210 100% 47%) 100%)',
        'gradient-blue': 'linear-gradient(135deg, hsl(210 100% 47%) 0%, hsl(240 100% 60%) 100%)',
        'gradient-orange': 'linear-gradient(135deg, hsl(25 95% 53%) 0%, hsl(15 95% 55%) 100%)',
      },
      animation: {
        'fade-in': 'fadeIn 0.4s ease-out',
        'slide-up': 'slideUp 0.4s ease-out',
        'pulse-glow': 'pulseGlow 2s ease-in-out infinite',
      },
      keyframes: {
        fadeIn: {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' },
        },
        slideUp: {
          '0%': { opacity: '0', transform: 'translateY(10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
        pulseGlow: {
          '0%, 100%': { boxShadow: '0 0 10px hsl(25 95% 53% / 0.2)' },
          '50%': { boxShadow: '0 0 20px hsl(25 95% 53% / 0.4)' },
        },
      },
    },
  },
  plugins: [animate],
};

export default config;
