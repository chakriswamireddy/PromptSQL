/**
 * Design token package — reserved for Phase 4 (Admin Console).
 * Phase 0 establishes the package so admin-console and future customer-facing
 * apps share a single theming source from day one.
 */

export const colors = {
  brand: {
    primary: "#0057FF",
    secondary: "#00C2A8",
  },
  semantic: {
    success: "#22C55E",
    warning: "#F59E0B",
    error: "#EF4444",
    info: "#3B82F6",
  },
} as const;

export const spacing = {
  xs: "4px",
  sm: "8px",
  md: "16px",
  lg: "24px",
  xl: "32px",
  "2xl": "48px",
} as const;

export type ColorToken = typeof colors;
export type SpacingToken = typeof spacing;
