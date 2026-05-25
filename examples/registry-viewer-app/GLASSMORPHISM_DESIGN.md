# Glassmorphism Design Guide

This document captures the CSS techniques and best practices for glassmorphism used in the TruvaG3 Registry Viewer application. Based on 2026 web design trends and research.

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

## Brand Mark (TruvaG3 Two-Tone)

The "TruvaG3" word in the page header uses a two-tone gradient that
mirrors the project logo: icy/glass blue for "Truva", warm amber for
"G3". Each part is its own `<span>` so the gradients can be applied
independently.

### Brand color tokens

Defined in `static/css/tokens.css`. The `*-light` and `*-dark` shades
feed a 135° gradient that gives the metallic 3D feel of the logo.

```css
:root {
  /* "Truva" — icy/glass blue */
  --brand-truva:       #8FCBED;
  --brand-truva-light: #DBEEFB;
  --brand-truva-dark:  #4A8AB8;
  /* "G3" — warm amber */
  --brand-g3:          #FFA040;
  --brand-g3-light:    #FFD080;
  --brand-g3-dark:     #C56710;
}
```

### HTML markup

Two adjacent spans, **no whitespace between them**. The descriptor word
("Registry") inherits the parent `<h1>` styling.

```html
<h1>
  <span class="brand-truva">Truva</span><span class="brand-g3">G3</span>
  Registry
</h1>
```

### CSS rules

Two flavors exist across the workspace, with a shared base and an
optional emphasis layer. **The property order inside each rule matters**
— see Gotchas below.

**Base — used by `examples/chat-ui/*.html`** (inline in each `<style>`).
The chat-ui's parent `<h1>` / `.sidebar-brand` is already large and
heavy, so the brand mark only needs the gradient + the gap fix:

```css
.brand-truva {
  /* 1. Gradient FIRST (the shorthand resets background-clip). */
  background: linear-gradient(135deg, var(--brand-truva-light), var(--brand-truva-dark));
  /* 2. Clip + transparent fill AFTER, so the gradient paints only the text shape. */
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
  /* 3. Tighten the right edge so "a" sits flush against the following "G". */
  letter-spacing: -0.04em;
}
.brand-g3 {
  background: linear-gradient(135deg, var(--brand-g3-light), var(--brand-g3-dark));
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
}
```

**With emphasis — used by registry-viewer's `static/css/layout.css`.**
The registry-viewer's parent `.logo h1` is only 20px / weight 600, so
the brand mark needs an extra size + weight bump to dominate the
trailing descriptor (`Registry`):

```css
.brand-truva {
  background: linear-gradient(135deg, var(--brand-truva-light), var(--brand-truva-dark));
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
  font-size: 1.35em;     /* 1.35 × 20px = 27px against parent h1 */
  font-weight: 800;
  letter-spacing: -0.04em;
}
.brand-g3 {
  background: linear-gradient(135deg, var(--brand-g3-light), var(--brand-g3-dark));
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
  font-size: 1.35em;
  font-weight: 800;
}
```

**Picking which:** if the parent header is already large enough that the
brand mark visibly dominates without help, use the base rule. If the
brand mark is competing with a similarly-sized descriptor word, add the
emphasis bump. Tune `1.35em` / `800` to taste.

### Gotchas (don't reintroduce)

**1. The `background:` shorthand resets `background-clip` to its
default.** If you write:

```css
.brand-x {
  -webkit-background-clip: text;       /* ← set first */
  background: linear-gradient(...);    /* ← shorthand RESETS clip to border-box */
}
```

…the gradient paints the whole bounding box (visible symptom: solid
color blocks where the text should be, because the gradient covers the
glyphs and `text-fill-color: transparent` makes the glyphs invisible
on top of it). **Always declare `background:` BEFORE `*-clip` and
`text-fill-color`.** Same trap when refactoring shared properties: if
`.brand-truva, .brand-g3 { background-clip: text; }` runs *before*
`.brand-truva { background: ...; }`, the per-class rule resets the
clip. Either keep all properties per-class, or use `background-image:`
(longhand, doesn't reset clip).

**2. Don't pair `.brand-truva` / `.brand-g3` with bare-element selectors
like `.logo h1 span`.**

The bare-selector specificity (`0,1,2` — class + element + element)
beats `.brand-truva`'s (`0,1,0` — single class). The bare rule silently
overrides the brand class for every `<span>` inside the header,
regardless of class. Symptom: both halves of the brand mark render in
the same color (whichever the bare rule set).

**3. Adjacent gradient spans get a visible gap from `background-clip:
text`.**

Each span paints its gradient into its own bounding box; natural glyph
side-bearings on the touching edges add ~4px of visible space between
"a" and "G" — even though there's no whitespace in the HTML. The
`letter-spacing: -0.04em` on `.brand-truva` closes that gap. Tune in
the range `-0.02em` (subtle) to `-0.06em` (aggressive) if your font
weight/size differs.

### Where the colors come from

Sampled from [TruvaG3-Logo.png](https://assets.truvag3.dev/TruvaG3-Logo.png).
If the canonical logo is ever updated, re-sample the mid-tone (label
center), highlight (brightest stroke), and shadow (deepest stroke) of
each colored word and update the six tokens above.

**Cross-UI consistency:** the same six tokens are duplicated inline in
each `examples/chat-ui/*.html`'s `<style>` block (dashboard, welcome,
index, devops, hitl, mock-sidebar). The **token values must stay in
sync** across all seven definitions — otherwise the brand mark drifts
between surfaces. The CSS rules themselves (`.brand-truva`,
`.brand-g3`) intentionally differ between chat-ui (base) and
registry-viewer (with emphasis); see "CSS rules" above.

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

  /* Brand colors (sampled from https://assets.truvag3.dev/TruvaG3-Logo.png — see "Brand Mark" §) */
  --brand-truva:       #8FCBED;  /* icy blue, mid-tone */
  --brand-truva-light: #DBEEFB;  /* icy blue, highlight */
  --brand-truva-dark:  #4A8AB8;  /* icy blue, shadow */
  --brand-g3:          #FFA040;  /* warm amber, mid-tone */
  --brand-g3-light:    #FFD080;  /* warm amber, highlight */
  --brand-g3-dark:     #C56710;  /* warm amber, shadow */
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
*Applied in: TruvaG3 Registry Viewer - Execution DAG Tab*
