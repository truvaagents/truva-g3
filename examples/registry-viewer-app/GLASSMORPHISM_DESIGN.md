# Glassmorphism Design Guide

This document captures the CSS techniques and best practices for glassmorphism used in the Truva-G3 Registry Viewer application. Based on 2026 web design trends and research.

## What is Glassmorphism?

Glassmorphism is a minimalistic design trend that creates a frosted glass-like appearance for UI elements. It provides:
- Visual depth through transparency and blur
- Clear hierarchy between foreground and background elements
- Modern, premium aesthetic that reduces visual complexity

## Core CSS Properties

### The Essential Glass Effect

```css
.glass {
  /* Semi-transparent background */
  background: rgba(255, 255, 255, 0.1);

  /* Frosted blur effect */
  backdrop-filter: blur(10px) saturate(180%);
  -webkit-backdrop-filter: blur(10px) saturate(180%);

  /* Subtle border for definition */
  border: 1px solid rgba(255, 255, 255, 0.2);

  /* Rounded corners */
  border-radius: 16px;

  /* Depth shadow */
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.2);
}
```

### Property Breakdown

| Property | Purpose | Typical Values |
|----------|---------|----------------|
| `backdrop-filter: blur()` | Creates frosted glass effect | 8-20px |
| `backdrop-filter: saturate()` | Enhances color vibrancy | 150-200% |
| `background` | Base transparency | rgba with 0.05-0.2 alpha |
| `border` | Edge definition | 1px solid with low alpha |
| `box-shadow` | Depth perception | Multiple layers |
| `border-radius` | Softened edges | 12-20px |

## Dark Theme Glassmorphism

Dark themes require adjusted values for proper visibility:

```css
.glass-dark {
  /* Darker, more opaque background */
  background: linear-gradient(
    135deg,
    rgba(255, 255, 255, 0.08) 0%,
    rgba(255, 255, 255, 0.02) 100%
  );

  /* Enhanced blur for dark backgrounds */
  backdrop-filter: blur(16px) saturate(180%);
  -webkit-backdrop-filter: blur(16px) saturate(180%);

  /* Subtle light border */
  border: 1px solid rgba(255, 255, 255, 0.12);

  /* Multi-layer shadows for depth */
  box-shadow:
    0 8px 32px rgba(0, 0, 0, 0.3),
    inset 0 1px 0 rgba(255, 255, 255, 0.1),
    inset 0 -1px 0 rgba(0, 0, 0, 0.1);
}
```

### Dark Theme Tips
- Use higher opacity values (0.08-0.15) than light themes
- Always use white or light gray text
- Add inset shadows for premium depth feel
- Consider gradient backgrounds over flat colors

## Apple's Liquid Glass Effect (2025-2026)

Apple introduced the "Liquid Glass" design language in WWDC 2025. Key techniques:

```css
.liquid-glass {
  position: relative;
  background: rgba(255, 255, 255, 0.15);
  backdrop-filter: blur(2px) saturate(180%);
  border: 1px solid rgba(255, 255, 255, 0.8);
  border-radius: 2rem;

  /* Dual shadow for depth */
  box-shadow:
    0 8px 32px rgba(31, 38, 135, 0.2),
    inset 0 4px 20px rgba(255, 255, 255, 0.3);
}

/* Pseudo-element for additional depth layer */
.liquid-glass::after {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background: rgba(255, 255, 255, 0.1);
  border-radius: inherit;
  backdrop-filter: blur(1px);
  box-shadow:
    inset -10px -8px 0px -11px rgba(255, 255, 255, 1),
    inset 0px -9px 0px -8px rgba(255, 255, 255, 1);
  opacity: 0.6;
  z-index: -1;
  filter: blur(1px) drop-shadow(10px 4px 6px black) brightness(115%);
}
```

## Implementation in Registry Viewer

### DAG Visualization Container

```css
.dag-viz-container {
  display: flex;
  flex-direction: column;
  height: 100%;

  /* Gradient glass background */
  background: linear-gradient(
    180deg,
    rgba(15, 15, 25, 0.6) 0%,
    rgba(10, 10, 18, 0.8) 100%
  );

  backdrop-filter: blur(12px);
  -webkit-backdrop-filter: blur(12px);
  border-radius: 16px;
  border: 1px solid rgba(255, 255, 255, 0.08);

  /* Premium shadow with inner glow */
  box-shadow:
    0 8px 32px rgba(0, 0, 0, 0.3),
    inset 0 1px 0 rgba(255, 255, 255, 0.04);

  overflow: hidden;
}
```

### Step Cards with Status Glow

```css
.dag-step-card {
  position: relative;

  /* Gradient glass effect */
  background: linear-gradient(
    135deg,
    rgba(255, 255, 255, 0.08) 0%,
    rgba(255, 255, 255, 0.02) 100%
  );

  backdrop-filter: blur(16px) saturate(180%);
  -webkit-backdrop-filter: blur(16px) saturate(180%);

  border-radius: 16px;
  padding: 20px;
  border: 1px solid rgba(255, 255, 255, 0.12);

  /* Multi-layer shadows */
  box-shadow:
    0 8px 32px rgba(0, 0, 0, 0.2),
    inset 0 1px 0 rgba(255, 255, 255, 0.1),
    inset 0 -1px 0 rgba(0, 0, 0, 0.1);

  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
}

/* Top highlight line using pseudo-element */
.dag-step-card::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 1px;
  background: linear-gradient(
    90deg,
    transparent,
    rgba(255, 255, 255, 0.2),
    transparent
  );
  border-radius: 16px 16px 0 0;
}

/* Status-specific colored glow */
.dag-step-card.completed {
  border-left: 3px solid var(--accent-green);
  box-shadow:
    0 8px 32px rgba(0, 0, 0, 0.2),
    inset 0 1px 0 rgba(255, 255, 255, 0.1),
    -6px 0 24px -10px rgba(50, 215, 75, 0.4);
}
```

### Interactive Hover States

```css
.dag-step-card:hover {
  /* Intensify the glass effect */
  background: linear-gradient(
    135deg,
    rgba(255, 255, 255, 0.12) 0%,
    rgba(255, 255, 255, 0.04) 100%
  );

  border-color: rgba(255, 255, 255, 0.18);

  /* Subtle lift effect */
  transform: translateY(-2px);

  /* Enhanced shadow on hover */
  box-shadow:
    0 16px 48px rgba(0, 0, 0, 0.25),
    inset 0 1px 0 rgba(255, 255, 255, 0.15),
    inset 0 -1px 0 rgba(0, 0, 0, 0.1);
}
```

### Legend Items with Glow Dots

```css
.dag-legend-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 14px;
  background: rgba(255, 255, 255, 0.04);
  border-radius: 20px;
  border: 1px solid rgba(255, 255, 255, 0.06);
  transition: all 0.2s;
}

.dag-legend-item:hover {
  background: rgba(255, 255, 255, 0.08);
  transform: translateY(-1px);
}

.dag-legend-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  /* Double glow effect for status dots */
  box-shadow: 0 0 10px currentColor, 0 0 20px currentColor;
}

.dag-legend-dot.completed {
  background: var(--accent-green);
  color: rgba(50, 215, 75, 0.6);
}
```

## Best Practices

### 1. Performance Optimization

```css
/* Limit glassmorphic elements - they're GPU intensive */
/* Use sparingly: 2-3 elements per viewport max */

/* For lower-powered devices, reduce blur */
@media (prefers-reduced-motion: reduce) {
  .glass {
    backdrop-filter: blur(4px);
  }
}

/* Avoid animating backdrop-filter directly */
/* Instead, animate opacity or transform */
.glass-transition {
  transition: transform 0.3s, opacity 0.3s;
  /* NOT: transition: backdrop-filter 0.3s; */
}
```

### 2. Browser Compatibility

```css
/* Always include webkit prefix */
.glass {
  backdrop-filter: blur(10px);
  -webkit-backdrop-filter: blur(10px);
}

/* Provide fallback for unsupported browsers */
@supports not (backdrop-filter: blur(10px)) {
  .glass {
    background: rgba(20, 20, 30, 0.95);
  }
}
```

### 3. Accessibility

```css
/* Ensure sufficient contrast */
.glass-text {
  color: #ffffff;
  /* Add text shadow for readability over varied backgrounds */
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.3);
}

/* Never use dark text on dark glass */
/* White or light gray text only */
```

### 4. Background Requirements

Glassmorphism works best with:
- Gradient backgrounds with color variation
- Images or patterns behind the glass
- Multiple overlapping glass elements

```css
/* Good background for glass elements */
body {
  background: linear-gradient(
    135deg,
    #0d0d12 0%,
    #1a1a2e 25%,
    #16213e 50%,
    #1a2744 75%,
    #0d0d12 100%
  );
}
```

## Color Palette Used

```css
:root {
  /* Glass backgrounds */
  --glass-bg: rgba(10, 10, 15, 0.85);
  --glass-bg-light: rgba(20, 20, 30, 0.75);

  /* Borders */
  --glass-border: rgba(255, 255, 255, 0.08);
  --glass-border-light: rgba(255, 255, 255, 0.12);

  /* Shadows */
  --glass-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);

  /* Blur amount */
  --glass-blur: 24px;

  /* Accent colors for status indicators */
  --accent-green: #32d74b;
  --accent-red: #ff6b6b;
  --accent-orange: #ffb340;
  --accent-blue: #0a84ff;
  --accent-purple: #da8fff;
  --accent-teal: #64d2ff;
}
```

## Resources

- [Glass UI - CSS library for glassmorphism](https://ui.glass/)
- [CSS Glassmorphism Generator](https://css.glass/)
- [60 CSS Glassmorphism Examples](https://freefrontend.com/css-glassmorphism/)
- [Liquid Glass Effect (Apple-inspired)](https://github.com/kevinbism/liquid-glass-effect)
- [Dark Glassmorphism 2026 Guide](https://medium.com/@developer_89726/dark-glassmorphism-the-aesthetic-that-will-define-ui-in-2026-93aa4153088f)
- [Glassmorphism CSS Generator by terrific.tools](https://www.terrific.tools/code/css-glassmorphism-generator)

---

*Last updated: January 2026*
*Applied in: Truva-G3 Registry Viewer - Execution DAG Tab*
