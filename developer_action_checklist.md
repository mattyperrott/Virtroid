# Developer Action Checklist

## Purpose

This document converts the design specification and inconsistency audit into an implementation-focused correction plan. It is organized by priority and by screen so a developer or AI coding agent can systematically bring the current app UI into alignment with the target design.

---

## 1. Global Fixes (Apply Everywhere First)

### 1.1 Reduce border visibility
- Lower default card border opacity significantly.
- Default borders should be barely perceptible.
- Selected cards may use mint border/glow, but default cards should rely mostly on surface contrast.

### 1.2 Standardize the spacing system
Implement and reuse these tokens everywhere:

- `page_padding = 24dp`
- `section_gap_major = 24–30dp`
- `card_gap = 14–18dp`
- `card_padding_lg = 22dp`
- `card_padding_md = 20dp`
- `title_to_subtitle = 6–8dp`
- `label_to_component = 12dp`
- `icon_to_text = 14–16dp`
- `row_height = 96dp`
- `cta_height = 84dp`
- `icon_button = 56dp`
- `dock_height = 92dp`
- `fab_size = 88dp`

### 1.3 Standardize radii
Use one radius system everywhere:

- large cards: `30–32dp`
- medium rows/cards: `28dp`
- icon wells: `22–24dp`
- pill CTAs: `999dp`
- circular actions: full circular

### 1.4 Standardize typography hierarchy
- Use Inter everywhere.
- Reduce body text prominence.
- Keep titles bold but not oversized.
- Make section labels quieter and more secondary.
- Ensure subtitles and metadata stay muted.

### 1.5 Unify icon style
- Replace thick/generic icons with thin outline icons.
- Ensure consistent stroke width.
- Make icon wells slightly quieter and more integrated.

### 1.6 Replace generic system modal/dialog styling
- Do not allow any identity/security modal to use platform-default light theme dialogs.
- Build a custom dark modal or full-screen flow.
- Match background, text, corner radii, spacing, and buttons to the design system.

---

## 2. Priority Order

### Priority 1 — Must Fix First
- Reduce border prominence globally
- Standardize spacing/padding/radii
- Tighten typography hierarchy
- Replace system/default modal styling

### Priority 2 — Structural Screen Fixes
- Rebuild New Runtime screen structure to match target
- Fix bottom dock and center FAB on My Runtimes
- Refactor Controls danger area into target architecture

### Priority 3 — Polish
- Refine icon set
- Add proper verified/recommended badge styling
- Improve connected display top shell
- Improve naming hierarchy and product tone

---

## 3. Screen-by-Screen Checklist

# 3.1 Generate Identity

## Fix hierarchy
- Reduce intro body text size and brightness.
- Increase title-to-body refinement.
- Keep the screen feeling premium and calm.

## Fix top icon tile
- Make the shield tile smaller and less ring-heavy.
- Reduce outline prominence.

## Fix info cards
- Reduce border contrast on Account ID and Device Fingerprint cards.
- Use softer surfaces instead of obvious outlines.
- Preserve 20dp padding and 30dp radius.

## Fix Verified badge
- Convert plain “Verified” text into a badge/chip.
- Use a subtle dark pill with mint dot and muted text.

## Fix action architecture
- Remove or redesign the side-by-side `Use Existing Access` / `Create Identity` row if the goal is to match the target flow.
- The desired flow is:
  1. intro
  2. identity info cards
  3. local encryption options
  4. single primary CTA

## Fix encryption option cards
- Tone down selected mint border and fill intensity.
- Reduce subtitle prominence.
- Make radio icon treatment lighter and more elegant.
- Keep left icon disc and selection alignment consistent.

## Fix bottom CTA spacing
- Ensure 24dp bottom padding above gesture area.
- Make CTA feel anchored, not clipped.

---

# 3.2 My Runtimes

## Fix header composition
- Reduce dominance of top-right action buttons.
- Keep the title block visually primary.

## Fix runtime card shell
- Soften the default border.
- Increase breathing room between title/status/stats/action.
- Make the card feel less like a dashboard panel.

## Fix runtime naming presentation
- If dynamic IDs remain, visually style them more intentionally.
- Consider productized display names where appropriate.

## Fix status row
- Improve dot/text baseline alignment.
- Soften secondary text treatment.

## Fix stats row
- Improve structure and consistency.
- Add clearer but still subtle separators.
- Avoid placeholder states looking unfinished.

## Fix start button
- Make the Start action more substantial and visually intentional.
- Ensure proper vertical alignment with the trash action.

## Fix dock and center FAB
- Rebuild dock to match target floating bar.
- Increase visual depth and polish.
- Make the center FAB clearly raised, white, and visually important.
- Rebalance nav icon and label sizing.

## Fix empty-space composition
- Rebalance vertical layout so large blank areas feel intentional.
- Avoid the screen feeling sparse without structure.

---

# 3.3 Controls

## Fix top bar tone
- Replace heavy/generic icons with more refined ones.
- Improve alignment and button prominence balance.

## Fix identity presentation
- Replace admin-console feeling with a more premium runtime identity block.
- Refine subtitle toward secure-runtime context.
- Improve emblem/avatar treatment.

## Fix status tiles
- Reduce visual heaviness.
- Refine padding and internal hierarchy.
- Make them feel premium, not blunt.

## Fix parameter rows
- Reduce border visibility.
- Improve icon well integration.
- Refine text alignment and subtitle balance.
- Ensure right chevrons/toggles align consistently.

## Fix danger architecture
- Collapse separate `Wipe` and `Destroy` actions into the target’s single bottom danger treatment if matching the reference is the goal.
- Tone down danger saturation.
- Remove extra yellow treatment if not required.

---

# 3.4 New Runtime

## Fix information architecture
- Rename `SESSION MODEL` to `THREAT MODEL`.
- Add both threat model options:
  - `Standard Persistent`
  - `Amnesic / Ephemeral`

## Fix selected option behavior
- Add `RECOMMENDED` badge to selected ephemeral card.
- Use softer mint border and glow.

## Fix grouped toggle section
- Convert standalone toggle row into grouped settings card.
- Include at least:
  - Force Tor Routing
  - Audio Passthrough

## Fix toggle styling
- Use correct dark/mint toggle palette.
- Refine track/thumb proportions.

## Fix CTA color
- Change `Provision Runtime` to white CTA on this screen to match the target.

## Fix spacing
- Reduce awkward emptiness by using proper section structure and spacing.
- Keep content vertical rhythm consistent.

---

# 3.5 Connected Display / Runtime View

## Fix top shell
- Simplify and premiumize the top control bar.
- Reduce visible outline framing.
- Improve overall spacing and tone.

## Fix title block
- Refine runtime title and subtitle styling.
- Make the status line smaller and more polished.

## Fix power button styling
- Tone down ring effect.
- Keep danger/stop affordance but make it more elegant.

## Fix action icons
- Replace thick/generic icons with thinner, cleaner iconography.
- Make control grouping more subtle.

## Fix framing of remote surface
- Improve the shell’s polish so the streamed runtime feels intentionally framed.
- If the remote runtime content remains visually old/generic, the shell must be strong enough to absorb that mismatch.

---

# 3.6 Password / Identity Modal

## Replace completely
- Do not keep the current light system-style modal.

## Implement custom modal/sheet
Use:
- dark surface background
- proper title hierarchy
- readable body text
- integrated input field
- mint-accent actions
- large radii
- correct padding
- no clipping/truncation

## Minimum requirements
- Title fully readable
- Body text wraps correctly
- Input field styled to design system
- Buttons aligned and visually integrated
- No light-theme default components

---

## 4. Component Fix Checklist

### Cards
- [ ] Lower border opacity
- [ ] Use surface contrast more than outlines
- [ ] Standardize padding and radius
- [ ] Add subtle gradients where appropriate

### Typography
- [ ] Set Inter as the font family
- [ ] Reduce body text brightness
- [ ] Normalize title sizes and weights
- [ ] Standardize section label style

### Icons
- [ ] Replace generic/thick icons
- [ ] Normalize icon container sizes
- [ ] Use consistent stroke weight

### Buttons
- [ ] Standardize CTA heights
- [ ] Ensure icon+text are centered as a group
- [ ] Differentiate mint vs white primary CTA by screen
- [ ] Make secondary actions feel intentional, not weak

### Toggles / Radios / Chips
- [ ] Normalize toggle sizes and colors
- [ ] Reduce radio heaviness
- [ ] Implement proper badge chips for status states

### Navigation
- [ ] Rebuild bottom dock geometry
- [ ] Strengthen center FAB
- [ ] Normalize nav icon and label balance

### Dialogs / Overlays
- [ ] Replace default modal styling
- [ ] Ensure all overlays match the app theme

---

## 5. Suggested Implementation Sequence

### Phase 1 — Foundation
1. Define tokens for spacing, colors, radii, typography, shadows, icon sizes
2. Update base card/button/input/toggle components
3. Replace icon set
4. Build custom modal component

### Phase 2 — High-Impact Screens
1. My Runtimes
2. New Runtime
3. Controls

### Phase 3 — Identity Flow
1. Generate Identity
2. Unlock Identity
3. Password / identity modal

### Phase 4 — Connected Runtime Surface
1. Top shell refinement
2. Remote surface framing
3. Final polish

---

## 6. Definition of Done

The UI is ready when:

- borders are subtle and controlled
- spacing is consistent across screens
- typography hierarchy feels premium
- icons feel like one system
- New Runtime architecture matches the target
- My Runtimes dock/FAB matches the target composition
- Controls screen no longer feels like a generic settings/admin screen
- all dialogs and overlays belong to the same design system
- the app reads as one cohesive premium runtime product, not a collection of good but inconsistent screens
