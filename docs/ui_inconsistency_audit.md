# UI Inconsistency Audit

## Scope

This audit compares the current app UI screenshots against the original target design reference screens and documents the inconsistencies in layout, style, spacing, hierarchy, components, and overall visual language.

---

## 1. Cross-App Inconsistencies

### 1.1 Borders are too visible
**Current build**
- Most cards and rows have pale visible borders.
- The outline is often one of the first things noticed.

**Target design**
- Borders should be very subtle.
- Cards should be defined mostly by surface tone and depth, not by obvious outlines.

**Impact**
- Makes the UI feel more like a prototype or settings app than a premium surface system.

---

### 1.2 Surfaces are too flat and too uniform
**Current build**
- Cards often share similar fill values and similar outline intensity.
- Selected states do not separate enough through softness and glow.

**Target design**
- Subtle tonal layering and restrained glow should distinguish surfaces.
- Selected cards should feel softly emphasized, not merely outlined.

**Impact**
- Reduces perceived depth and premium finish.

---

### 1.3 Typography hierarchy is inconsistent
**Current build**
- Some titles are too large or too heavy.
- Some body text is too bright and too visually strong.
- Section labels are more dominant than intended.

**Target design**
- Tighter hierarchy between display titles, card titles, labels, metadata, and body copy.
- Secondary text should be calmer and less intrusive.

**Impact**
- The product feels less refined and more utilitarian.

---

### 1.4 Corner radius system is inconsistent
**Current build**
- Some elements feel overly capsule-shaped.
- Some cards and buttons do not feel like part of one geometry system.

**Target design**
- Large cards: rounded rectangles
- Action buttons: pills
- Icon actions: circles
- Rows: large soft rectangles

**Impact**
- Weakens the sense of a cohesive design language.

---

### 1.5 Spacing rhythm is inconsistent
**Current build**
- Some top sections are cramped.
- Some cards feel slightly under-padded.
- Some large empty areas do not feel intentional.

**Target design**
- More deliberate vertical rhythm
- Stronger internal padding
- More consistent section separation

**Impact**
- The screens feel assembled rather than composed.

---

### 1.6 Icon style is inconsistent
**Current build**
- Some icons look generic or default.
- Stroke weights vary.
- Some icon containers are too visually loud.

**Target design**
- Thin outline icons
- Rounded terminals
- Minimal consistent stroke
- More restrained icon wells

**Impact**
- Reduces perceived polish.

---

### 1.7 UI still reads as “good prototype” instead of finished system
**Current build**
- Direction is correct, but consistency and refinement are not fully there.

**Target design**
- Quiet, unified, premium, intentional, and productized.

---

## 2. Screen Audit — Generate Identity

### 2.1 Top icon container too large and too ring-heavy
**Current**
- Shield container is large and strongly outlined.

**Target**
- Smaller, more refined tile with less emphasis on the outline.

---

### 2.2 Intro paragraph too large and too bright
**Current**
- Body text is visually heavy and wraps broadly.

**Target**
- Smaller, quieter secondary copy with better hierarchy and calmer line spacing.

---

### 2.3 Account ID and Device Fingerprint cards too outlined
**Current**
- Strong pale borders make them feel like form fields.

**Target**
- Softer surface treatment with very faint borders.

---

### 2.4 Verified badge is not styled as a badge
**Current**
- “Verified” appears as plain text.

**Target**
- Verified should be presented as a compact pill chip, ideally with a mint dot and subtle background.

---

### 2.5 Mid-screen two-button row does not match target architecture
**Current**
- `Use Existing Access` and `Create Identity` appear side by side.

**Target**
- The target screen flows into Local Encryption selection followed by one dominant bottom CTA.
- The two-button row changes hierarchy and weakens the intended composition.

---

### 2.6 Local Encryption copy is too verbose and off-target
**Current**
- Copy is longer and more technical than the target.

**Target**
- Shorter, more polished, calmer explanation.

---

### 2.7 PIN and Passphrase option cards are too outline-heavy
**Current**
- Selected card border is too bright.
- Fill is too green/strong.
- Subtitle text is too prominent.
- Selection control is visually heavy.

**Target**
- Softer selected state
- More restrained mint emphasis
- Smaller, calmer supporting text
- More elegant icon/selection treatment

---

### 2.8 Bottom CTA spacing appears too tight
**Current**
- The bottom CTA is clipped/close to the bottom edge in the screenshot.

**Target**
- More deliberate bottom breathing room and anchoring.

---

## 3. Screen Audit — My Runtimes

### 3.1 Header action buttons too large and too dominant
**Current**
- Top-right circular actions draw too much attention.

**Target**
- They should be present but secondary to the title/header block.

---

### 3.2 Runtime card too dense and too boxed
**Current**
- Strong outline
- Internal structure feels tight and dashboard-like

**Target**
- Softer shell
- Clearer hierarchy
- More breathing room between content rows

---

### 3.3 Runtime naming feels generic
**Current**
- Uses technical/generated runtime ID style (`Runtime 36297121`)

**Target**
- More productized naming style such as `Primary Nexus` / `Ephemeral Burner`
- Even if dynamic IDs remain, hierarchy should visually feel more intentional and branded

---

### 3.4 Status row lacks refinement
**Current**
- Functional but utilitarian

**Target**
- Better dot/text alignment
- More elegant secondary tone and spacing

---

### 3.5 Stats row feels unfinished
**Current**
- Labels and values are spread apart but not designed with strong structure
- `--` placeholders contribute to a temporary/unpolished feel

**Target**
- Stronger stat architecture with clearer separators and intentional layout

---

### 3.6 Start button too weak
**Current**
- Feels like a thin secondary row control

**Target**
- More substantial pill action with stronger presence inside the runtime card

---

### 3.7 Bottom nav dock does not match target composition
**Current**
- Dock feels flat and low-energy
- Center FAB is visually weak
- Nav items are too dim/small
- Dock feels more like a prototype footer than a premium floating dock

**Target**
- Strong floating dock presence
- Large raised white center FAB
- Better active/inactive balance
- More sculpted geometry

---

### 3.8 Too much dead space without compositional balance
**Current**
- Large empty middle area between the card and dock

**Target**
- Spacious, but visually intentional

---

## 4. Screen Audit — Controls

### 4.1 Header iconography feels generic
**Current**
- Back and top-right icons are heavy and generic

**Target**
- More restrained, premium utility controls

---

### 4.2 Identity presentation is wrong in tone
**Current**
- Large phone icon avatar and runtime ID subtitle create an admin-console feeling

**Target**
- More secure-runtime identity presentation
- More premium emblem treatment
- More productized subtitle, such as secure network/runtime context

---

### 4.3 Status tiles are too boxy and visually heavy
**Current**
- Tiles take up too much attention and vertical space
- Text is blunt and utilitarian

**Target**
- Equal tiles but with more elegance, better spacing, and softer emphasis

---

### 4.4 Semantic tone differs from secure target
**Current**
- `TUNNEL: Direct`

**Target**
- The target design emphasizes secure/encrypted network posture
- The actual state may differ, but it visually shifts the whole screen away from the intended secure aesthetic

---

### 4.5 Parameter rows are too outlined
**Current**
- Rows look like settings cards with obvious strokes

**Target**
- Softer embedded row surfaces with faint separation

---

### 4.6 Row layout feels generic settings UI
**Current**
- Functional row layout but not premium enough

**Target**
- Better icon well integration, subtler alignment, more refined internal spacing

---

### 4.7 Danger actions are split incorrectly
**Current**
- Separate yellow `Wipe` card
- Separate red `Destroy` card

**Target**
- One cohesive bottom danger action: `Wipe & Destroy`

---

### 4.8 Danger styling is too loud
**Current**
- Red danger card is more saturated than target
- Yellow wipe card introduces extra palette noise

**Target**
- Danger should be muted, elegant, and still serious

---

## 5. Screen Audit — New Runtime

### 5.1 Wrong section naming
**Current**
- Uses `SESSION MODEL`

**Target**
- Uses `THREAT MODEL`

**Impact**
- Changes the security framing of the screen

---

### 5.2 Missing dual-option structure
**Current**
- Only one option card is present (`Ephemeral by Default`)

**Target**
- Two stacked threat model choices:
  - `Standard Persistent`
  - `Amnesic / Ephemeral`

---

### 5.3 Missing `RECOMMENDED` badge
**Current**
- No badge on selected option

**Target**
- Selected ephemeral option includes a subtle recommended badge

---

### 5.4 Toggle group architecture missing
**Current**
- `Audio Passthrough` appears as a standalone row

**Target**
- Grouped settings block with multiple toggles such as Force Tor Routing and Audio Passthrough

---

### 5.5 Toggle styling mismatched
**Current**
- Toggle colors and proportions feel disconnected from the design system

**Target**
- Dark inactive, mint active, premium restrained proportions and contrast

---

### 5.6 Provision Runtime CTA color incorrect
**Current**
- Mint CTA

**Target**
- White CTA on the target screen

---

### 5.7 Screen density too sparse
**Current**
- Large empty area after minimal content

**Target**
- Still spacious, but with richer structure due to two option cards and grouped toggles

---

### 5.8 Close button too heavy
**Current**
- Slightly oversized/heavy relative to the screen title

**Target**
- Still prominent but more visually balanced

---

## 6. Screen Audit — Connected Display / Runtime View

### 6.1 Top shell too outlined and toolbar-like
**Current**
- Rounded framed bar with visible outline

**Target**
- Cleaner, calmer, premium top control strip

---

### 6.2 Title block feels less productized
**Current**
- `Runtime 36297121`
- `Secure • 720x1600`

**Target**
- More polished product-style naming and tighter status styling

---

### 6.3 Power button too ring-heavy
**Current**
- Strong red ring treatment

**Target**
- Still distinct, but more restrained

---

### 6.4 Right-side icons too generic/thick
**Current**
- Camera/download/sliders icons feel heavier and more default

**Target**
- Thinner, more elegant icon family

---

### 6.5 Divider too literal
**Current**
- Divider between controls reads like a utility toolbar

**Target**
- More subtle grouping

---

### 6.6 Remote runtime surface clashes with shell
**Current**
- The remote Android-like surface appears old and generic
- The shell is not polished enough to frame that mismatch convincingly

**Target**
- A stronger shell that can visually contain the runtime surface with intent

---

## 7. Screen Audit — Password Dialog / Modal

### 7.1 Modal is completely outside the design system
**Current**
- Light gray system-style dialog
- Default-looking layout and input treatment

**Target**
- Custom dark modal or custom app-designed sheet/screen matching the main UI

---

### 7.2 Typography and contrast are broken
**Current**
- Title nearly disappears
- Body text clips and overflows
- Action labels feel weak

**Target**
- Readable dark-surface modal with strong hierarchy and coherent copy styling

---

### 7.3 Layout is visibly broken
**Current**
- Truncation/clipping
- Poor spacing
- Input area feels unintegrated

**Target**
- Fully designed modal layout with deliberate padding and reliable text constraints

---

### 7.4 System modal breaks premium illusion
**Current**
- Even if primary screens are close, this modal instantly exposes unfinished implementation

**Target**
- No generic system dialogs for security-critical flows

---

## 8. Priority Fix Order

### Priority 1
- Reduce default border opacity across cards
- Standardize radii, padding, spacing, and row heights
- Tighten typography hierarchy
- Replace the generic system modal with a custom dark modal/sheet

### Priority 2
- Fix bottom dock and center FAB on My Runtimes
- Rebuild New Runtime screen structure to match target
- Refine Controls screen row/card architecture and danger action design

### Priority 3
- Unify icon family and stroke weight
- Improve status chips/badges
- Refine connected display top shell

---

## 9. Short Summary

The current build is directionally correct but still too prototype-like.

### Main recurring issues
- borders are too visible
- cards are too outline-driven
- typography is not refined enough
- spacing rhythm is inconsistent
- some screens have the wrong architecture vs target
- buttons, toggles, icons, and badges are not fully unified
- bottom dock/FAB is much weaker than target
- Controls screen feels like a settings/admin UI
- system modal breaks the design language entirely

### Desired end state
A cohesive, premium, low-contrast, security-first runtime UI with:
- soft surface separation
- refined spacing
- consistent geometry
- restrained mint accents
- better hierarchy
- custom-designed overlays and dialogs
