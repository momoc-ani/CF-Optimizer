---
name: CF Optimizer Design System
colors:
  surface: '#f6faff'
  surface-dim: '#cfdce7'
  surface-bright: '#f6faff'
  surface-container-lowest: '#ffffff'
  surface-container-low: '#ebf5ff'
  surface-container: '#e3effb'
  surface-container-high: '#ddeaf5'
  surface-container-highest: '#d8e4f0'
  on-surface: '#111d25'
  on-surface-variant: '#40484f'
  inverse-surface: '#26323b'
  inverse-on-surface: '#e6f2fe'
  outline: '#707880'
  outline-variant: '#bfc7d0'
  surface-tint: '#00658f'
  primary: '#005d85'
  on-primary: '#ffffff'
  primary-container: '#1677a6'
  on-primary-container: '#edf6ff'
  inverse-primary: '#87ceff'
  secondary: '#556069'
  on-secondary: '#ffffff'
  secondary-container: '#d6e1ec'
  on-secondary-container: '#59646d'
  tertiary: '#7c4d00'
  on-tertiary: '#ffffff'
  tertiary-container: '#9c6309'
  on-tertiary-container: '#fff2e7'
  error: '#ba1a1a'
  on-error: '#ffffff'
  error-container: '#ffdad6'
  on-error-container: '#93000a'
  primary-fixed: '#c8e6ff'
  primary-fixed-dim: '#87ceff'
  on-primary-fixed: '#001e2e'
  on-primary-fixed-variant: '#004c6d'
  secondary-fixed: '#d9e4ef'
  secondary-fixed-dim: '#bdc8d3'
  on-secondary-fixed: '#121d25'
  on-secondary-fixed-variant: '#3d4851'
  tertiary-fixed: '#ffddb8'
  tertiary-fixed-dim: '#ffb961'
  on-tertiary-fixed: '#2b1700'
  on-tertiary-fixed-variant: '#663e00'
  background: '#f6faff'
  on-background: '#111d25'
  surface-variant: '#d8e4f0'
typography:
  title-lg:
    fontFamily: Inter, system-ui
    fontSize: 22px
    fontWeight: '600'
    lineHeight: 32px
  title-md:
    fontFamily: Inter, system-ui
    fontSize: 20px
    fontWeight: '600'
    lineHeight: 28px
  body-default:
    fontFamily: Inter, system-ui
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 20px
  body-sm:
    fontFamily: Inter, system-ui
    fontSize: 13px
    fontWeight: '400'
    lineHeight: 18px
  label-caps:
    fontFamily: Inter, system-ui
    fontSize: 12px
    fontWeight: '500'
    lineHeight: 16px
    letterSpacing: 0.02em
  data-mono:
    fontFamily: JetBrains Mono, monospace
    fontSize: 13px
    fontWeight: '400'
    lineHeight: 18px
rounded:
  sm: 0.125rem
  DEFAULT: 0.25rem
  md: 0.375rem
  lg: 0.5rem
  xl: 0.75rem
  full: 9999px
spacing:
  base: 4px
  xs: 4px
  sm: 8px
  md: 12px
  lg: 16px
  xl: 24px
  gutter: 16px
  margin: 24px
---

## Brand & Style
The design system is engineered for high-utility desktop operations, prioritizing precision, technical clarity, and task-oriented focus. The aesthetic is **Corporate / Modern** with a lean towards **Minimalism**, stripping away decorative elements to favor data density and functional transparency.

The UI should evoke a sense of reliability and quiet efficiency. It is designed to feel like a native system utility rather than a marketing-led web application. Visual hierarchy is established through structural alignment and semantic color coding rather than depth or shadows.

## Colors
The palette is built on a neutral gray foundation to maintain a "low-temperature" environment for prolonged work sessions.

- **Primary Action**: Used for calls-to-action, active selection states, and primary navigation indicators.
- **Semantic Accents**: Applied sparingly to status indicators and process feedback. Success, Warning, and Danger colors must meet WCAG 2.1 AA contrast ratios against both light and dark surface variables.
- **Neutral/Inactive**: Used for disabled states and secondary metadata.
- **Charts**: Use a distinct Violet accent (#7950F2) alongside the primary blue for trend lines to differentiate analytical data from interactive UI components.

## Typography
This design system utilizes a **System UI font stack** (Inter preferred) to ensure zero-latency font loading and a native application feel.

- **Data Tables**: Use `data-mono` (tabular numerals) for IP addresses, latency figures, and throughput metrics to ensure vertical alignment and quick scanning.
- **Hierarchy**: Use `title-lg` for primary view headers and `title-md` for modal/sidebar headers.
- **Density**: Default body text for the desktop experience is set to 14px, with a 13px variant for sidebars and dense property grids.
- **Language Support**: For Simplified Chinese status strings, ensure the font stack falls back to "Microsoft YaHei" or "PingFang SC" to maintain legibility at small sizes.

## Layout & Spacing
The layout uses a **Fluid Grid** model optimized for wide-screen desktop displays. Spacing is strictly based on a 4px increment system (Mantine-compatible).

- **Navigation Rail**: A fixed 64px width icon rail on the far left for top-level navigation.
- **Main Layout**: Employs a sticky header for toolbars and a sticky footer for the persistent task strip.
- **Tables**: Use a 12px horizontal padding for cells (`md`) and 8px vertical padding (`sm`) to maximize visible rows.
- **Gaps**: Use `lg` (16px) for major layout sections and `md` (12px) for grouping related controls within a section.

## Elevation & Depth
This design system avoids heavy shadows and skeuomorphism. It utilizes **Tonal Layers** and **Low-Contrast Outlines** to define hierarchy.

- **Background to Surface**: The background (`#F6F7F8` / `#15191D`) acts as the lowest layer. Surface containers (`#FFFFFF` / `#1D2227`) sit on top with a 1px solid border (`#DDE2E6` / `#353C42`).
- **Drawers and Dialogs**: Use a very subtle, large-radius ambient shadow (e.g., `0 4px 20px rgba(0,0,0,0.08)`) only when an element overlaps critical content.
- **Active States**: Highlighting is achieved through primary color backgrounds or 2px thick border indicators on the left side of active navigation items.

## Shapes
The shape language is disciplined and geometric.

- **Containers & Inputs**: A consistent 6px border radius is applied to all buttons, input fields, and cards.
- **Status Badges**: Use a fully rounded (pill-shaped) radius only for status indicators (e.g., "已验证", "运行中") to differentiate them from interactive buttons or selectable chips.
- **Data Selection**: Segmented controls and tabs should use the 6px radius for the container, with the internal active segment matching the container’s corner radius logic.

## Components

### Data Tables
- **Headers**: Sticky, semi-bold text, using the surface border color for a bottom stroke.
- **Cells**: Use `data-mono` for all numerical data.
- **Density**: Rows are compact (approx. 32px height). Hover states use a 5% opacity tint of the primary color.

### Status Badges
- **Language**: Use Simplified Chinese labels.
- **Logic**:
  - **Success (已验证 / 已连接)**: Light green background with Success color text + icon.
  - **Processing (正在连接 / 验证中)**: Light primary background with primary text + spinning loader icon.
  - **Error (验证失败 / 失败)**: Light red background with Danger color text + alert icon.

### Navigation Rail
- **Style**: Dark-themed background even in light mode to provide a strong visual anchor.
- **Icons**: 24px stroke-based icons. Active state includes a 2px vertical primary color bar on the left edge.

### Persistent Task Strip
- **Position**: Fixed at the bottom of the viewport.
- **Content**: Shows current "Run" status (e.g., "TCP 初筛 - 45%").
- **Visuals**: Uses a thin progress bar at the very top edge of the strip (1-2px height).

### Input Fields & Controls
- **Toolbars**: Dense 32px height buttons and segmented controls.
- **Segmented Controls**: Inset style with a subtle background toggle.
- **Fields**: 1px solid border, shifts to 1px Primary color on focus with a 2px soft outer glow.

### Side Drawers
- **Interaction**: Slide-in from the right. Used for IP details or node selection configurations.
- **Header**: Includes a "Close" button and a primary action button at the top-right.