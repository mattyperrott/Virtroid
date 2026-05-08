# UI Design Specification

## Purpose

This specification defines the full visual and layout system for the secure runtime mobile UI based on the target reference screens and the detailed design analysis completed in this conversation. It is intended to guide implementation so the UI is consistent, premium, restrained, security-focused, and visually aligned across all screens.

---

## 1. Design Intent

### Product feel
The UI should feel:

- secure
- private
- cryptographic
- premium
- minimal
- calm
- low-contrast
- technical
- hardened
- deliberate

### Aesthetic direction
The app is a **premium secure cloud-runtime controller** with a **sovereign/private OS aesthetic**.

It should feel like:

- a hardened runtime management shell
- a private encrypted cloud control plane
- a premium dark system UI
- a calm, expensive, restrained product

It must **not** feel like:

- generic Material Design
- stock Android settings
- consumer fintech
- flashy cyberpunk neon
- playful SaaS
- admin dashboard panels
- default system dialogs

---

## 2. Core Visual Principles

1. **Low contrast, not low clarity**  
   The UI should use restrained contrast while still preserving hierarchy.

2. **Surface-driven, not border-driven**  
   Cards and panels should primarily be separated by subtle surface tone shifts, not obvious outlines.

3. **Generous spacing**  
   The UI must breathe. Vertical rhythm and padding are critical.

4. **Consistent geometry**  
   Large cards, circular actions, pill CTAs, and rounded icon wells must follow a unified radius system.

5. **Mint accent only for state**  
   Mint green is reserved for active, connected, selected, secure, verified, and emphasized states.

6. **Quiet premium typography**  
   Use clear, modern grotesk typography with tight hierarchy and controlled weight.

7. **Thin icon language**  
   Icons must be outline-based, rounded, minimal, and consistent in stroke weight.

---

## 3. Color System

### Base palette

| Token | Value | Usage |
|---|---:|---|
| `bg.app` | `#0B0E0C` | App background |
| `bg.surface` | `#171B18` | Primary card surface |
| `bg.surfaceAlt` | `#1B201D` | Alternate surface |
| `bg.surfaceDeep` | `#131715` | Deep inset surface |
| `border.surface` | `rgba(210,230,218,0.08)` | Default very subtle borders |
| `border.selected` | `rgba(169,220,181,0.65)` | Selected cards |
| `divider.default` | `rgba(255,255,255,0.06)` | Internal dividers |
| `text.primary` | `#F3F5F2` | Main headings and key text |
| `text.secondary` | `#8F9891` | Body copy and metadata |
| `text.tertiary` | `#737B75` | Labels, captions |
| `icon.muted` | `#A9B0AB` | Inactive icons |
| `accent.mint` | `#A9DCB5` | Active/selected/secure |
| `accent.mintBright` | `#B8E8C1` | Stronger mint emphasis |
| `accent.mintDark` | `#8BC49A` | Deeper active green |
| `glow.mint` | `rgba(169,220,181,0.16)` | Selected glow |
| `status.active` | `#A9DCB5` | Status dot active |
| `status.inactive` | `#66706A` | Status dot inactive |
| `danger.bg` | `#3A1919` | Danger background |
| `danger.border` | `rgba(232,107,107,0.18)` | Danger border |
| `danger.text` | `#F06F6F` | Danger icon/text |
| `cta.whiteFill` | `#F3F3F3` | White primary CTA |
| `cta.whiteText` | `#111311` | Text on white CTA |
| `badge.verifiedBg` | `rgba(255,255,255,0.03)` | Verified chip bg |
| `overlay.glass` | `rgba(255,255,255,0.02)` | Glass/highlight layer |

### Color usage rules

- Do not use bright saturated colors except restrained mint and muted danger red.
- Default cards should not have bright borders.
- Inactive toggles, icons, and metadata must remain subdued.
- The UI should never drift into blue-gray; keep the palette slightly green/olive-neutral.

---

## 4. Typography

### Font family

Primary: `Inter`  
Fallback: `SF Pro Display`, `system-ui`, sans-serif

### Type scale

| Token | Size | Weight | Tracking | Usage |
|---|---:|---:|---:|---|
| `type.display` | 32sp | 700 | -0.03em | Large hero titles |
| `type.screenTitle` | 28sp | 700 | -0.02em | Screen titles |
| `type.cardTitle` | 20–22sp | 600 | -0.01em | Card titles |
| `type.sectionLabel` | 12sp | 600 | 0.10em | Uppercase section labels |
| `type.body` | 16sp | 400 | 0 | Body text |
| `type.bodySmall` | 14sp | 400 | 0 | Secondary copy |
| `type.statValue` | 18sp | 600 | 0 | Stats |
| `type.button` | 19sp | 700 | -0.01em | CTA buttons |
| `type.nav` | 13sp | 500 | 0 | Bottom nav labels |
| `type.meta` | 15–16sp | 400 | 0 | Status / subtitle text |

### Typography rules

- Primary text should be off-white, not pure white.
- Secondary copy should be slightly smaller and noticeably quieter.
- Section labels must be uppercase and subdued.
- Long paragraphs should use calm line height and readable wrapping.
- Avoid overly heavy body text.

---

## 5. Spacing System

Use a strict 4dp/8dp grid.

### Layout tokens

| Token | Value |
|---|---:|
| `space_4` | 4dp |
| `space_6` | 6dp |
| `space_8` | 8dp |
| `space_10` | 10dp |
| `space_12` | 12dp |
| `space_14` | 14dp |
| `space_16` | 16dp |
| `space_18` | 18dp |
| `space_20` | 20dp |
| `space_22` | 22dp |
| `space_24` | 24dp |
| `space_28` | 28dp |
| `space_30` | 30dp |
| `space_32` | 32dp |
| `space_36` | 36dp |

### Global spacing rules

- Horizontal page padding: **24dp**
- Top content start: **16–28dp below safe area**, depending on screen
- Bottom content padding: **24dp above gesture area**
- Floating bottom nav bottom inset: **16dp above bottom safe area**
- Major section gap: **24–30dp**
- Card stack gap: **14–18dp**
- Section label to component group gap: **12dp**
- Title to subtitle gap: **6–8dp**
- Subtitle/body to next group: **18–28dp**
- Icon to text gap: **14–16dp**
- Internal group gap in large cards: **18–22dp**

---

## 6. Corner Radius System

| Token | Value | Usage |
|---|---:|---|
| `radius_22` | 22dp | Icon wells |
| `radius_28` | 28dp | Medium cards / rows |
| `radius_30` | 30dp | Large cards |
| `radius_32` | 32dp | Prominent large cards |
| `radius_pill` | 999dp | Pill buttons / chips / CTA |

### Rules

- Large cards: 30–32dp
- Medium rows/cards: 26–28dp
- Icon wells: 22–24dp
- Circular icon buttons: full circular
- Bottom dock: 36dp visual radius
- Center FAB: circular

---

## 7. Surfaces, Borders, and Shadows

### Default card styling

- Background: very dark charcoal surface
- Optional subtle diagonal gradient
- Extremely faint border
- No harsh drop shadows
- Minimal outer glow only where needed

### Default card CSS-like reference

```css
background: linear-gradient(135deg, rgba(255,255,255,0.03), rgba(255,255,255,0.01));
background-color: #171B18;
border: 1px solid rgba(210,230,218,0.08);
border-radius: 30px;
box-shadow: 0 8px 30px rgba(0,0,0,0.22);
```

### Selected card styling

```css
border-color: rgba(169,220,181,0.65);
box-shadow: 0 0 0 1px rgba(169,220,181,0.10), 0 0 24px rgba(169,220,181,0.10);
```

### Rules

- Default borders must be very subtle.
- The UI should not look outline-heavy.
- Selected states should glow softly, not scream.
- Danger cards should use muted dark red, not bright red.

---

## 8. Icon System

### Icon style

- Thin outline icons
- Rounded line terminals
- Approx. 1.75–2.0dp stroke
- Minimal, geometric, premium
- No thick default Material-looking icons

### Sizes

| Element | Size |
|---|---:|
| Small inline icon | 18–20dp |
| Standard icon | 20–22dp |
| Top bar circular action icon | 22–24dp |
| Icon well content | 22–26dp |

### Icon containers

- Circular action buttons: 52–56dp
- Rounded square/circle wells inside cards: 46–54dp
- Leading runtime icon wells: 72dp where applicable

---

## 9. Buttons

### Primary mint CTA

| Property | Value |
|---|---:|
| Height | 72–86dp |
| Radius | pill |
| Fill | `#A9DCB5` |
| Text | `#111311` |
| Weight | 700 |
| Glow | soft mint glow |

### White CTA

| Property | Value |
|---|---:|
| Height | 84dp |
| Radius | pill |
| Fill | `#F3F3F3` |
| Text | `#111311` |
| Content | centered icon + text group |

### Secondary dark pill button

- Dark surface
- Subtle border only if needed
- Used for actions like Start or secondary utility buttons
- Must still feel substantial, not thin-outline only

### Button alignment rules

- Full-width CTAs span container width minus internal padding.
- Label content is centered as a group.
- Icon + text group should be optically centered.
- Chevron/right arrows sit 12–16dp from adjacent text or internal edge.

---

## 10. Toggles, Radios, Status Chips

### Toggle

| Property | Value |
|---|---:|
| Width | ~52dp |
| Height | ~32dp |
| Active Track | mint |
| Inactive Track | dark charcoal |
| Thumb Diameter | ~24dp |
| Active Thumb | dark |
| Inactive Thumb | muted gray |

### Radio selection

- Outer ring: 2px visual
- Selected state: mint border and subtle fill or inner dot
- Unselected state: muted gray-green

### Verified / recommended badges

- Compact pill chip
- Dark subtle fill
- Small mint dot if applicable
- Text 12–13sp, medium weight
- Never just plain floating text without a badge surface

---

## 11. Alignment Rules

- Default content is left-aligned to a consistent 24dp page grid.
- Only explicitly centered screen titles should be centered.
- All section labels, cards, and text blocks align to the same left edge.
- Subtitle text should start exactly under the title above it.
- Right-side controls (toggles, chevrons, action buttons) should align on a consistent vertical line across rows.
- Button contents must be centered as a group, not spread apart unless clearly designed that way.
- Status dots align optically with text baseline, not mathematically centered to line box.

---

## 12. Global Component Dimensions

| Component | Size / Height |
|---|---:|
| Circular icon button | 52–56dp |
| Large row | 92–96dp |
| Input | 84–88dp |
| Toggle group row | 72dp |
| Bottom CTA | 82–86dp |
| Runtime card CTA | 72dp |
| Keypad button | 104–116dp |
| FAB | 88dp |
| Bottom dock | 92dp |

---

## 13. Screen Specifications

# 13.1 My Runtimes

### Layout
- Horizontal padding: 24dp
- Top safe-area gap: 18dp
- Bottom reserved area for dock: minimum 132dp

### Header
- Small label: `SECURE CLIENT`
- Label to title gap: 6dp
- Title: `My Runtimes`
- Two top-right circular action buttons, 56dp each
- Gap between those buttons: 12dp

### Header to first card
- 28dp

### Runtime card
- Radius: 32dp
- Padding: 22dp
- Top row to stats row: 18dp
- Stats row to CTA: 22dp

### Top row
- Leading icon well: 72dp
- Gap to text block: 16dp
- Top-right utility button: 50dp
- Title to status gap: 6dp

### Status row
- Dot size: 8dp
- Dot to text gap: 8dp

### Stats row
- Three evenly distributed columns
- Labels uppercase muted gray
- Label to value gap: 8dp
- Very subtle vertical separators

### CTA
- Height: 72dp
- Full width
- Mint fill for active primary runtime

### Between cards
- 18dp

### Bottom nav dock
- Height: 92dp
- Side inset: 24dp
- Bottom inset: 16dp above safe area
- Center FAB overlaps dock by ~18dp
- FAB: 88dp, white fill, black plus

---

# 13.2 New Runtime

### Layout
- Horizontal padding: 24dp
- Top safe-area gap: 16dp
- Bottom CTA pinned with 24dp bottom padding

### Top bar
- Close button left
- Screen title centered: `New Runtime`
- Top bar height zone: 56dp

### Top bar to first section
- 32dp

### Identity section
- Label: `IDENTITY`
- Label to input gap: 12dp
- Input height: 86dp
- Input horizontal inset: 18dp
- Input radius: 32dp

### Threat model section
- Gap from identity section: 26dp
- Label: `THREAT MODEL`
- Two stacked option cards
- Each option card padding: 20dp
- Height target: 126–138dp
- Gap between cards: 14dp
- Radio to text gap: 16dp
- Title to description gap: 8dp
- Selected option uses mint border/glow
- Include `RECOMMENDED` badge on selected ephemeral option

### Toggle group
- Gap from option cards: 20dp
- Single grouped container
- Row height: 72dp
- Row horizontal inset: 20dp
- Divider inset: 20dp

### CTA
- Gap from toggle group: 28dp minimum
- White fill CTA
- Height: 84dp
- Content centered with rocket icon + label
- Icon to text gap: 12dp

---

# 13.3 Controls

### Layout
- Horizontal padding: 24dp
- Top safe-area gap: 16dp
- Bottom padding: 24dp

### Top bar
- Back button left: 56dp
- Right action button: 56dp
- Title centered: `Controls`
- Secondary pencil action below right button with ~18dp spacing if present

### Identity block
- Gap from top bar: 28dp
- Avatar emblem: 96dp
- Gap avatar to text: 18dp
- Title to subtitle gap: 6dp
- Subtitle style should reflect security/runtime context

### Status tiles
- Gap from identity block: 24dp
- Three equal tiles
- Gap between tiles: 14dp
- Padding: 18dp
- Icon well: 46dp
- Icon well to label: 18dp
- Label to value: 10dp

### Section label
- Gap from tiles: 28dp
- `SESSION PARAMETERS`

### Parameter rows
- Height: 96dp
- Horizontal padding: 20dp
- Radius: 28dp
- Gap between rows: 14dp
- Leading icon well: 50dp
- Icon to text gap: 16dp
- Title to subtitle gap: 6dp
- Right chevron/toggle aligned consistently

### Danger card
- Gap from final normal row: 22dp
- Height: 108–116dp
- Padding: 22dp
- Muted red surface
- Use single `Wipe & Destroy` bottom danger action in the target design system

---

# 13.4 Connected Display View

### Top shell
- Height: 96dp
- Horizontal padding: 20dp
- Left power button: 56dp
- Gap to title block: 16dp
- Title to status gap: 4dp
- Right action buttons: 52dp each
- 12dp gaps between right-side buttons
- Divider, if any, must be very subtle

### Remote display
- Begins directly under top shell
- Treated as a full remote-rendered surface
- App shell must feel premium enough to frame the runtime surface cleanly

### Within remote surface mock
- Search pill side inset: ~56dp
- Search pill height: ~68dp
- Search pill to dock gap: ~32dp
- Bottom dock side inset: ~52dp
- Bottom dock height: ~96dp
- Gesture bar inset: ~12dp

---

# 13.5 Generate Identity

### Layout
- Horizontal padding: 24dp
- Top safe-area gap: 18dp
- Bottom CTA padding: 24dp

### Top icon tile
- Size: 56dp
- Margin below to title: 28dp

### Title block
- Title: `Generate Identity`
- Title to intro copy gap: 14dp
- Body copy line spacing: relaxed and calm
- Use muted secondary text

### Info cards
- Gap from intro to first card: 28dp
- Padding: 20dp
- Radius: 30dp
- Gap between info cards: 16dp
- Label to value: 14dp
- Copy icon inset: minimal
- Verified badge top-right as pill chip

### Local Encryption section
- Gap from info cards: 30dp
- Title to body copy: 8dp
- Body copy to options: 20dp

### Option cards
- Height: 122–132dp
- Padding: 20dp
- Gap between cards: 16dp
- Left icon disc: 54dp
- Gap icon to text: 16dp
- Title to subtitle: 6dp
- Right radio vertically centered
- Selected PIN card gets subtle mint border/glow

### CTA
- Gap from options: 28dp
- Full-width mint CTA
- Height: 84dp
- Text + arrow centered as a group
- Arrow gap from text: 12dp

---

# 13.6 Unlock Identity

### Layout
- Horizontal padding: 24dp
- Centered composition
- Top safe-area gap: 36dp
- Bottom padding: 24dp

### Lock emblem
- Size: 96dp
- Margin from safe area: ~40dp
- Margin to title: 30dp

### Title + status
- Title centered: `Unlock Identity`
- Title to status row: 14dp
- Status icon to text: 10dp

### PIN dots
- Gap from status row: 28dp
- Dot size: 18dp
- Gap between dots: 16dp
- First filled, remaining dim/hollow based on entry state

### Keypad
- Gap from dots: 36dp
- 3 equal columns
- Button size: 110dp
- Horizontal gap: 20dp
- Vertical gap: 18dp
- Number to letters gap: 6dp

### Footer action
- Gap from keypad: 28dp
- Bottom centered uppercase text link
- 14sp, increased tracking

---

## 14. Hard Constraints for Implementation

- Do not use platform default spacing.
- Do not use generic system dialogs for security/identity flows.
- Do not use visible bright outlines for all cards.
- Do not use generic settings-row styling.
- Do not overuse mint.
- Do not improvise colors or radii screen by screen.
- Use shared tokens for all spacing, radii, typography, buttons, and icon sizes.
- Match the target architecture, not just the visual vibe.

---

## 15. Implementation Summary

This UI system should read as a cohesive, premium, encrypted-runtime operating surface.  
Its quality depends less on flashy effects and more on disciplined consistency:

- subtle borders
- controlled typography
- soft premium spacing
- disciplined mint accent usage
- consistent geometry
- high-quality component reuse
- no generic defaults
