# tbound web chat: DeepSeek-like design language (spec, not brand)

Status: the current visual-language spec. It began as a read-only analysis and proposal, and it modified no tbound file. Until Phase 3 completes, `extensions/webchat/docs/webchat/mockups/tbound-redesign-v2.html` is the structural layout reference only; its visual language is superseded by this spec.
Upstream: `deepseek-ai/deepseek-harness` @ `d743267` (a local shallow clone). Treated as untrusted data; nothing from it was executed.
Target: `codex/tbound-web-chat`, `extensions/webchat/internal/webchat/assets/styles.css` (1611 lines; the move has landed).

Evidence tags: **[src]** verified in a file (path:line). **[calc]** computed by `contrast.mjs` (WCAG 2.2 math, sRGB). **[inf]** my inference or judgement. **[gen]** general knowledge, not in either repo.

---

## 0. Bottom line

- **Borrow the structure, not the identity.** Token tiers, the 4/8/12/16/20/28 radius scale, 14/22 body text, hairline-first elevation, colour reserved for state, 100–300 ms motion, and a global reduced-motion kill switch are generic patterns [src: `base.css:14-23`, `gradient-shadow-text.css:5-38`].
- **Do not borrow:** the name or any "DeepSeek"/"DSH" wording as product identity, the whale or wordmark, the blue `#4176E6` accent and focus ring, the onboarding violet/blue/cyan gradients, the Montserrat headings, their exact bluish-tinted neutrals, or their blue user-bubble tint. Section 1 has the quotes.
- **Mood:** quiet and near-monochrome. White canvas, near-black primary action, 0.5 px hairlines and very soft shadows, flat transcript rows with one filled user bubble, 14 px text, and colour used only for state and source.
- **tbound today** is dark-only, uses oklch chroma up to 0.14 on source chips, has uppercase 10 px mono chips and one 10 px radius everywhere [src: `styles.css:14-97`, `:1172-1188`, `index.html:6`]. The proposal keeps every governance semantic and every token name (`--state-*`, `--source-*`). Only the values and a light theme change.
- **Contrast:** all 24 checked pairs pass in both proposed themes [calc]. Today's dark sheet fails two pairs: deny chip text (4.00:1) and the input boundary (1.47:1) [calc].

---

## 1. Brand and licence: what may and may not be copied

Sources: `BRAND_GUIDELINES.md`, `LICENSE`, `THIRD_PARTY_NOTICES.md`, `packages/client/ui-theme/src/styles/Montserrat-OFL.txt`.

| Class | Item | Status | Evidence |
|---|---|---|---|
| Code and tokens | CSS values and structure | **MIT, free to use.** The notice must be kept if text is copied verbatim. | `LICENSE`: "Copyright (c) 2026 DeepSeek … The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software." |
| Name | "DeepSeek Harness" | **Do not use as a product or project name.** Descriptive use is allowed. | `BRAND_GUIDELINES.md`: "avoid using the full "DeepSeek Harness" trademark directly… It may also involve trademark infringement." Allowed: "built on DeepSeek Harness" or "compatible with DeepSeek Harness." |
| Name | "DSH" | Their recommended abbreviation for their own ecosystem. Do not adopt it. | `BRAND_GUIDELINES.md`: "we recommend using the abbreviated "DSH" designation" |
| Logos and marks | whale, wordmark, favicons, app icons, `running-whale@2x.png`, `welcome-brand.svg`, `website/public/wordmark.svg` | **Do not copy.** Brand materials. | `BRAND_GUIDELINES.md`: "avoid using official brand materials… so as not to give users the false impression of official endorsement." |
| Brand colour | `deepseek-*` ramp, `#4176E6` accent, `--dsw-alias-link`, focus ring `state-business-primary`, onboarding `#3964FE` gradients | **Do not copy.** Brand identity. | `design-platform.css:22-31, 191, 227, 240, 358`; `onboarding.css:3-6` |
| Brand font | Montserrat (Light/Regular/Medium) for "brand headings" | **Do not use.** The OFL allows it, but their own docs call it the brand face. | `packages/client/ui-theme/src/styles/brand-font.css`; `packages/client/ui-theme/README.md:84`: "`--dsw-font-family-brand` selects this face for brand text; ordinary UI keeps the system font stack." |
| Neutral ramp | `neutral-bluish-*` (blue-tinted greys) | Structure is free. The exact blue-tinted values are part of their look, so use our own zero-chroma greys. | `design-platform.css:56-74`, `:166-282` |

Judgement: nothing in the licence stops a similar mood. The guideline's concern is confusion and implied endorsement [inf]. So: no DeepSeek name, marks, accent, or brand font. The visual similarity comes from the neutrals and geometry.

---

## 2. Upstream token tiers and values

**Tiers.** The 8 global sheets are loaded in this order by `ui-theme/src/client/styles.ts:13-21`: `base`, `corner-shape`, `design-platform`, `focus`, `onboarding`, `scrollbar`, `gradient-shadow-text`, `shiki`. Also `brand-font.css`, which is not in the list.
- **Static** (`design-platform.css:4-83` light, `:85-164` dark): raw swatches (`--dsw-static-*`). Re-declared per theme.
- **Alias** (`:166-282` light, `:284-398` dark): semantic roles (`--dsw-alias-*`). Re-declared per theme too.
- **Specific** (`--dsw-specific-*`): component-level bindings, e.g. `specific-bubble`, `specific-sidebar-fill`.
- Dark selector: `body[data-ds-dark-theme]` [src: `design-platform.css:85`]. Theme comes from a body attribute set by a boot script, not `prefers-color-scheme` alone [src: `ui-theme/README.md:44`].

**Values** (hex = sRGB, converted from the `rgb()` as written; upstream values are not oklch).

| Role | Upstream light | Upstream dark | Class | Evidence |
|---|---|---|---|---|
| Canvas (`bg-base`) | `#FFFFFF` | `#151517` | NEUTRAL | `:167`, `:285` |
| Layers 1–3 | `#FFFFFF` ×3 (elevation by stroke, not fill) | `#232324` / `#2C2C2E` / `#353638` | NEUTRAL | `:171-173`, `:289-291` |
| Sidebar fill | `#F9FAFB` | `#1B1B1C` | NEUTRAL | `:275`, `:393` |
| Module fill | `#F5F6F7` | `#353638` | NEUTRAL | `:179`, `:297` |
| Hover (solid) | `#F1F3F5` | `#353638` | NEUTRAL | `:212`, `:330` |
| Hover (alpha) | `rgba(38,49,72,.06)` (blue-slate cast) | `rgba(255,255,255,.08)` | NEUTRAL (light cast is brand-ish) | `:213`, `:331` |
| Text primary | `#0F1115` | `#F9FAFB` | NEUTRAL | `:222`, `:340` |
| Text secondary | `#61666B` | `#CFD3D6` | NEUTRAL | `:223`, `:341` |
| Text tertiary | `#81858C` (**3.71:1 on white, fails AA**) | `#ADB2B8` | NEUTRAL, but do not copy the light value | `:225`, `:343`; [calc] |
| Caption | `#ADB2B8` (2.13:1) | `#81858C` | NEUTRAL (decorative only) | `:214`, `:332`; [calc] |
| Border l1 / l2 / l3 / l4 | black 4% / 10% / 12% / 16% | white 6% / 12% / 16% / 20% | NEUTRAL | `:185-189`, `:303-307` |
| Primary button | fill `#0F1115`, label white | fill `#F9FAFB`, label dark | NEUTRAL (black-primary is the look) | `:192`, `:310`; screenshot |
| Link / focus / business | `#4176E6` | `#7AAAFF` | **BRAND** | `:227`, `:240`, `:345`, `:358` |
| Success | `#22C55E` | `#22C55E` | GENERIC | `:253`, `:371` |
| Error | `#EC1313` | `#F25A5A` | GENERIC | `:250`, `:368` |
| Warn | `#F59E0B` | `#F59E0B` | GENERIC | `:257`, `:375` |
| Idle | `#D4D4D4` (1.48:1, fails) | `#545557` | NEUTRAL, do not copy light | `:252`, `:370`; [calc] |
| Scrollbar thumb / hover | `#E5E5E5` / `#D4D4D4` | `#3C3C3D` / `#545557` | NEUTRAL | `:236-239`, `:354-357` |
| Menu fill | `rgba(248,249,250,.58)` + `blur(40px) saturate(150%)` | `rgba(67,69,74,.45)` | NEUTRAL | `:271`, `:389`; `gradient-shadow-text.css:20` |
| Modal mask | black 24% | black 50% | NEUTRAL | `:174`, `:292` |
| Code block | `#F9FAFB` (banner same) | `#1B1B1C` (banner `#2C2C2E`) | NEUTRAL | `:229-230`, `:347-348` |
| User bubble | `#EDF3FE` (deepseek-50 tint) | `#2C2C2E` | Light = BRAND tint; dark = NEUTRAL | `:268`, `:386` |
| Diff add / del (bg) | `#E6F4E7` / `#FCE6E2` | `#1F3124` / `#3C1F1B` | GENERIC | `:244-249`, `:362-367` |
| Skeleton | black 4% | white 8% | NEUTRAL | `:182`, `:300` |

**Geometry, type, motion** (theme-free)

| Token | Upstream | Evidence |
|---|---|---|
| Radii xs / sm / md / lg / xl / panel | 4 / 8 / 12 / 16 / 20 / 28 px | `base.css:18-23` |
| Radius pill | 999 px (badges only) | `HeroShell.module.css:67` |
| Corner curve | `corner-shape: superellipse(1.5)`, guarded by `@supports` | `corner-shape.css:16-22` |
| Body font stack | `-apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'Helvetica Neue', Helvetica, Arial, sans-serif` | `base.css:7` |
| Code font stack | `'SF Mono', 'JetBrains Mono', 'Fira Code', Consolas, 'Liberation Mono', Menlo, Courier, 'PingFang SC', 'Microsoft YaHei'`. No bare `monospace` tail, because Windows CJK otherwise falls back to SimSun. | `base.css:1-5` (comment), `base.css:11` |
| Sizes and line-heights | body 14/22 (bubble), markdown 14/24, secondary 13/20, small 12/18, micro 11/14, title 20/28, h1 21/30 (bold) | `MessageItem.module.css:33-34`; `gradient-shadow-text.css:93-96, 185-262` |
| Weights | 400 body, 500 labels and emphasis, 600 markdown strong, 700 headings. The Figma 510 weight is snapped to 500. | `design-platform.css:1-3`; `gradient-shadow-text.css` |
| Letter-spacing | none in the 8 sheets | grep, empty |
| Easing | `cubic-bezier(0.4, 0, 0.2, 1)` | `base.css:14` |
| Durations | 100 ms (colour), 200 ms (default), 300 ms (layout) | `base.css:15-17` |
| Z-index observed | 1 content, 4–6 sticky bars, 10–11 handles, 15 leading seat, 20 overlay, 100 tooltip, 1000 modal, 1100 portal tooltip | `TrajectoryToolbar.module.css:4`; `AppFrame.module.css:211, 231, 284`; `Tooltip.module.css:6, 25`; `Modal.module.css:10` |
| Focus ring | 2 px, colour from `--dsw-focus-ring-color`, falls back to brand blue | `focus.css:3-9` |
| Reduced motion | `prefers-reduced-motion: reduce` kills transitions in at least 25 module files | e.g. `AppFrame.module.css:52-55`, `StateDot.module.css:85-88`, `Modal.module.css:55` |

---

## 3. Component look (verified in source)

| Area | DeepSeek treatment | Evidence |
|---|---|---|
| App shell | 3 columns. Sidebar on `sidebar-fill` with a 0.5 px right hairline. Centre on canvas. Resize handle 8 px wide, z 11. Column animation 0.3 s, disabled while dragging. | `AppFrame.module.css:1-22, 58-63, 223-231` |
| User message | Filled rounded bubble: radius 20, padding 10/16, 14/22, capped at about 525 px. | `MessageItem.module.css:16-35` |
| Assistant text | No bubble class exists for assistant text, so it sits unboxed. | [inf] from `MessageItem.module.css` |
| Turn process row | Flat row, 33 px, 0.5 px bottom hairline, tertiary text, 14 px chevron. | `TurnProcessNodeView.module.css:1-34` |
| Trigger card | Radius 20, 0.5 px border at 6% alpha, header padding 12/16, body in 12 px. | `TurnTriggerNodeView.module.css:1-31` |
| Trajectory row | 38 px tall. Radius 12. 0.5 px border at 16% alpha. Fill `layer-3`. Source tag 19–22 px, radius 4, 10–13 px strong. 2 px turn rail, 3 px selection rail. Error rail tinted red. | `TrajectoryCell.module.css:4-47`; `TrajectoryTable.module.css:313-347, 450-486` |
| Trajectory toolbar | 32 px. 0.5 px bottom hairline. Toggles 20 px, radius 8, 12 px. Switch track 10 px. | `views.module.css:3`; `TrajectoryToolbar.module.css:7-37, 107-127` |
| Status dots | 8 px, hue per state, `currentColor`. Spinner 1.5 s linear, two animations. | `StateDot.module.css:2-44` |
| Composer | Radius 28. Stroke is a 0.5 px elevation (border-l2). Soft shadow. 14 px draft text. | `InputBar.module.css:45-68` |
| Tabs | Segmented: container padding 4, radius 16; tab 34 px. Indicator 0.5 px stroke. Selected label goes from 500 to 600 weight. | `SegmentedTabs.module.css:1-40`; `SegmentedControl.module.css:11-31` |
| Buttons | md 36 px (radius 12), sm 28 px (radius 8). Primary = near-black fill, white label. Outline = 0.5 px border at 12%. | `Button.module.css:2-60` |
| Inputs | 32 px, radius 12, 0.5 px border at 16%. Focus changes the border colour only. | `Input.module.css:5-13` |
| Menus | Translucent menu fill with 40 px blur. Elevation panel. Radius 16. | `MenuSurface.module.css:2-7`; `design-platform.css:271`; `gradient-shadow-text.css:20, 34-36` |
| Modal | Radius 28. Layer-2 fill. Prominent elevation. Enter 200 ms. | `Modal.module.css:18-47` |
| Tooltip | 13/20. Radius 8. Padding 3/7. Enter keyframe. Z 100 (portal 1100). | `Tooltip.module.css:1-25, 55-59` |
| Code block | Radius 16. Banner 9/14 padding. Label 11 px. Sticky banner. | `markdown/CodeBlock.module.css:4-48` |
| Scrollbar | 5 px WebKit thumb, round, transparent track. Standard path gated with `@supports`. | `scrollbar.css` (95 lines) |
| Empty state | 26/32 headline. "Preview" pill, radius 999, 12 px. Composer centred beneath. | `HeroShell.module.css:29-42, 62-73`; screenshot |
| Loading | Spinner (StateDot), text shimmer, skeleton at 4% black. | `StateDot.module.css:37-68`; `design-platform.css:182` |
| Icons | 14 px chevrons, 16 px leading icons, thin outline style in screenshots. | `TurnProcessNodeView.module.css:28-34`; `MessageItem.module.css:124-138`; screenshot |

### Mood summary
- **Density:** medium-tight. Rows 32–38 px. Gaps 4/6/8/12 px dominate (`px` frequency across client CSS: 8, 4, 6, 12, 10, 2, 16, 14 px) [src: `git grep` count].
- **Contrast:** calm. Primary text near black, secondary and tertiary greys. Colour only for state.
- **Border vs shadow:** hairline strokes first. Shadows are very faint (`0 3px 8px rgba(0,0,0,.03)`). Dark theme relies on stroke alone (`gradient-shadow-text.css:17-24`).
- **Rounded vs sharp:** rounded but not pill. 12–20 for controls and cards, 28 for composer and modal, 4–8 for chips. Superellipse corners.
- **Monochrome vs colourful:** monochrome base, accent only for links and business state.
- **Animation restraint:** 100 ms colour, 200–300 ms movement, everything disabled under reduced motion.

### Screenshots (paths only; not copied)
- `docs/user/guide/providers-models-page.png` (settings, light). Viewed: white panel, grey provider card, black Apply, outline Cancel.
- `packages/client/ui-settings-account/src/client/assets/onboarding-welcome.png` (dark and light variants also present). Viewed: illustration with the desktop app; the brand accent and gradients are part of it.
- Also `packages/client/ui-chat/src/client/chat/running-whale@2x.png` (brand, do not use), `docs/user/guide/providers-custom-form.png`.
- No chat-transcript screenshot exists in the repo. The chat look is therefore taken from CSS, not pixels.

---

## 4. tbound: current tokens (dark-only)

Source: `extensions/webchat/internal/webchat/assets/styles.css`. `index.html:6` sets `<meta name="color-scheme" content="dark">`. No `prefers-color-scheme`, no `data-theme`, no `@font-face`, no `url()`, no `http` URL in the assets [src: grep]. CSP: `style-src 'self'; font-src 'self'` [src: `handler.go:318`]. No `style=` attribute and no `.style.` / `setProperty` calls in the `.mjs` or HTML [src: grep, zero matches].

| Token | Value (oklch) | Lines | Role |
|---|---|---|---|
| `--background` / `--foreground` | .145 / .985 | 14–15 | canvas / text |
| `--card` / `--popover` | .205 / .269 | 16, 18 | panels / overlays |
| `--primary` / `--primary-foreground` | .922 / .205 | 22–23 | primary |
| `--secondary` / `--muted` / `--accent` | .269 / .269 / .371 | 24, 26, 28 | fills |
| `--muted-foreground` | .708 | 27 | secondary text |
| `--destructive` / `-foreground` | .704 .191 22 / .637 .237 25 | 32–33 | DENY line / ink |
| `--info` / `-foreground` | .62 .16 252 / .78 .13 252 | 34–35 | info |
| `--success` / `-foreground` | .70 .15 155 / .82 .14 155 | 36–37 | verified OK |
| `--warning` / `-foreground` | .80 .15 82 / .87 .13 82 | 38–39 | REPORTED / PENDING / UNKNOWN |
| `--border` / `--input` / `--ring` | 10% white / 15% white / .556 | 42–44 | hairline / input edge / focus |
| `--sidebar` / `--sidebar-border` | .18 / 10% white | 48, 52 | rail |
| `--font-sans` / `--font-mono` | Inter first, then system; Geist Mono first | 55–56 | type (neither font is bundled) |
| `--source-system` … `--source-message` | six oklch hues (.72–.83, chroma .04–.14) | 64–70 | record provenance |
| `--state-ok` … `--state-idle` | aliases of the above | 79–92 | state dots and chips |
| `--rail-width` / `--inspector-width` / `--row-pad-x` | 15rem / 21.5rem / .625rem | 95–97 | geometry |
| `--radius` | .625rem (10 px) | 45 | radius |

State encodings, shape plus colour [src: `styles.css:859-890, 1189-1230`]:
- **ok**: solid filled dot. Chip text "succeeded" / "completed".
- **allow**: inset ring dot in the allow tone. Chip text "allow".
- **deny**: solid filled dot in the deny tone. Chip "denied" / "failed".
- **pending**: hollow grey ring. Chip amber.
- **reported**: amber dashed border, plus an "unverified" marker.
- **unknown**: amber dotted border.
- **idle**: 55% grey filled dot.

**Gap found [src]:** `.dot-ok` and `.dot-deny` are both solid filled dots that differ only by colour (`styles.css:866, 872`). Both chips carry text, so the state is still legible. Any dot that appears without its label relies on colour alone. The proposal fixes this in the dot rules, see section 7.

---

## 5. Mapping: tbound → proposal

Names are kept. Values change. The proposal is oklch, with the sRGB hex that the contrast script actually used (gamut-mapped where the oklch fell outside sRGB).

| tbound token | Today (dark) | Proposed light | Proposed dark | Note |
|---|---|---|---|---|
| `--background` | .145 (#0A0A0A) | `oklch(1 0 0)` #FFFFFF | `oklch(0.18 0 0)` #121212 | canvas |
| `--foreground` | .985 | `oklch(0.2 0 0)` #161616 | `oklch(0.97 0 0)` #F5F5F5 | text primary |
| `--card` | .205 | `oklch(1 0 0)` #FFFFFF + hairline + soft elevation | `oklch(0.245 0 0)` #202020 | cards, composer |
| `--popover` | .269 | `oklch(1 0 0)` + panel elevation | `oklch(0.245 0 0)` + stroke | menus, tooltips |
| `--secondary` / `--muted` | .269 | `oklch(0.955 0 0)` #F0F0F0 | `oklch(0.275 0 0)` #282828 | user bubble, fills |
| `--muted-foreground` | .708 | `oklch(0.42 0 0)` #4D4D4D (secondary), `oklch(0.5 0 0)` #636363 (tertiary) | `oklch(0.82 0 0)` #C4C4C4 / `oklch(0.7 0 0)` #9E9E9E | secondary and tertiary text |
| `--accent` | .371 | `oklch(0.945 0 0)` #EDEDED | `oklch(0.29 0 0)` #2B2B2B | hover |
| `--primary` / `-foreground` | .922 / .205 | `oklch(0.2 0 0)` #161616 / `oklch(0.985 0 0)` #FAFAFA | `oklch(0.96 0 0)` #F2F2F2 / `oklch(0.2 0 0)` #161616 | primary action |
| `--destructive` (line) | .704 .191 22 | `oklch(0.52 0.19 27)` #BE2323 | `oklch(0.76 0.16 22)` #FF8987 | DENY dot |
| `--destructive-foreground` (ink) | .637 .237 25 (**4.00:1, fails**) | `oklch(0.46 0.18 27)` #A50E15 | `oklch(0.76 0.16 22)` #FF8987 | DENY chip text |
| `--info` / `-foreground` | .62 / .78 | `oklch(0.44 0.09 250)` #275582 | `oklch(0.76 0.09 250)` #85B6E9 | allow tone (see note) |
| `--success` (line) | .70 .15 155 | `oklch(0.52 0.13 150)` #1D7D3E | `oklch(0.76 0.15 155)` #52CD86 | OK dot |
| `--success-foreground` (ink) | .82 .14 155 | `oklch(0.44 0.12 150)` #03642B | `oklch(0.76 0.15 155)` #52CD86 | OK chip text |
| `--warning` (line) | .80 .15 82 | `oklch(0.6 0.15 70)` #B17000 | `oklch(0.8 0.14 80)` #EDB345 | REPORTED / UNKNOWN / PENDING line |
| `--warning-foreground` (ink) | .87 .13 82 | `oklch(0.45 0.11 70)` #774A00 | `oklch(0.86 0.13 80)` #FEC766 | amber chip text |
| `--border` | 10% white | `oklch(0.2 0 0 / 10%)` | `oklch(1 0 0 / 12%)` | hairline (decorative) |
| `--input` | 15% white (**1.47:1, fails**) | `oklch(0.6 0 0)` #808080 (**3.67:1 on sidebar**) | `oklch(0.6 0 0)` #808080 (4.11:1 on raised) | control boundary, must reach 3:1 |
| `--ring` | .556 | `oklch(0.2 0 0)` #161616 | `oklch(0.93 0 0)` #E8E8E8 | focus, 2 px. Not brand blue. |
| `--sidebar` | .18 | `oklch(0.975 0 0)` #F7F7F7 | `oklch(0.215 0 0)` #191919 | rail |
| `--sidebar-border` | 10% white | `oklch(0.2 0 0 / 10%)` | `oklch(1 0 0 / 12%)` | rail edge |
| `--source-system` | .72 .13 250 | = allow `#275582` | `#85B6E9` | permission |
| `--source-user` | .78 .12 210 | `oklch(0.42 0.09 210)` #005763 | `oklch(0.8 0.10 210)` #66CFE1 | |
| `--source-assistant` | .76 .14 300 | `oklch(0.5 0.13 300)` #6F4FA1 (4.60:1, thin margin) | `oklch(0.8 0.12 300)` #C9ACFF | |
| `--source-tool` | .81 .14 65 | = warn ink `#774A00` | `#FEC766` | |
| `--source-decision` | .83 .13 92 | `oklch(0.45 0.11 92)` #685300 | `oklch(0.86 0.13 92)` #F0CE65 | |
| `--source-result` / `-message` | .78 .04 250 | `oklch(0.42 0.03 250)` #414F5D | `oklch(0.78 0.03 250)` #A9B9CA | |
| `--state-idle` | 55% grey dot | `oklch(0.55 0 0)` #717171 **solid** | `oklch(0.62 0 0)` #868686 **solid** | a 55% fill fails 3:1 on light; use solid |
| `--state-pending-mark` | muted | `oklch(0.5 0 0)` #636363 | `oklch(0.72 0 0)` #A4A4A4 | hollow ring |
| `--radius` | .625rem | `--radius-md` 12 px | same | scale below |
| `--font-sans` | Inter, then system | system stack (section 9) | same | no webfont |

Note on ALLOW. Keep ALLOW in a blue-slate hue (250, chroma 0.09). It is lower chroma than the brand blue (about 0.2) and it is not green (OK is 150). ALLOW is also an inset ring, not a filled dot, so it is distinct in shape too. The `--state-allow` / `--info` / `--source-system` family stays one token family. It must never reuse the OK colour.

Note on AMBER. The hue moves from 82 to 70 in light. Yellow text on white cannot reach 4.5:1 [calc], so the amber family is darkened in light only. Dark keeps the original hue.

Note on PENDING vs IDLE. Pending is a hollow ring; idle is a solid filled dot. Both are grey, and they differ by fill, not colour. Idle carries no text label, so keep it visually faint but at 3:1 or above.

### Governance guardrails, checked
- ALLOW ≠ OK: hue differs (250 vs 150), shape differs (inset ring vs filled), text differs ("allow" vs "succeeded"). Pass in both themes.
- DENY: filled, red, text "denied". Deny dot must never appear without its label.
- OK (verified) vs REPORTED (unverified): green filled vs amber dashed border plus "unverified" marker. Shape carries the distinction, not colour.
- UNKNOWN: amber dotted border plus "unknown" text. PENDING: hollow grey ring plus "pending" text. IDLE: solid grey, no text.
- No state is conveyed by colour alone, provided every dot keeps its label. The recommended deny-dot change (square instead of circle) is an extra cue, not a substitute.

---

## 6. Contrast (WCAG 2.2 AA), computed [calc]

Method: `contrast.mjs` in this folder (run with `node`). oklch → sRGB via Ottosson's matrices. Out-of-gamut colours are reduced in chroma until they fit (CSS Color 4 style). Translucent layers are composited over the actual surface. Each element is checked against the worst surface it may sit on. Thresholds: text 4.5:1, non-text UI 3:1. Chips use the state colour at 12% over the surface, matching `color-mix(... 12%, transparent)` in `styles.css`. Output is in `contrast-output.txt` in the same folder.

Limits: this is maths, not a rendered browser check. Font weight and size are not modelled. Chip borders and shadows are not checked (decorative). Re-verify in a browser before shipping.

### Proposed light (24 pairs, all PASS)

| Element | Ink | Worst surface | Ratio | Need |
|---|---|---|---|---|
| Text primary | #161616 | hover #EDEDED | 15.41 | 4.5 |
| Text secondary | #4D4D4D | hover | 7.20 | 4.5 |
| Text tertiary | #636363 | hover | 5.11 | 4.5 |
| Chip OK (verified) | #03642B | hover | 5.37 | 4.5 |
| Chip DENY | #A50E15 | hover | 5.52 | 4.5 |
| Chip ALLOW | #275582 | hover | 5.52 | 4.5 |
| Chip REPORTED / UNKNOWN / PENDING | #774A00 | hover | 5.68 | 4.5 |
| Chip pending (text) | #4D4D4D | hover | 6.02 | 4.5 |
| Source user | #005763 | hover | 5.80 | 4.5 |
| Source assistant | #6F4FA1 | hover | 4.60 | 4.5 (thin) |
| Source tool | #774A00 | hover | 5.42 | 4.5 |
| Source result | #414F5D | hover | 6.00 | 4.5 |
| Source decision | #685300 | hover | 5.33 | 4.5 |
| Selection text | #161616 on #D7D7D7 | selection | 12.61 | 4.5 |
| Primary button label | #FAFAFA on #161616 | fill | 17.34 | 4.5 |
| Dot OK fill | #1D7D3E | hover | 4.42 | 3 |
| Dot DENY fill | #BE2323 | hover | 5.17 | 3 |
| Dot ALLOW ring | #275582 | hover | 6.59 | 3 |
| Pending hollow ring | #636363 | hover | 5.11 | 3 |
| REPORTED dashed border | #B17000 | hover | 3.46 | 3 |
| UNKNOWN dotted border | #B17000 | hover | 3.46 | 3 |
| Idle solid dot | #717171 | hover | 4.13 | 3 |
| Focus ring | #161616 | hover | 15.41 | 3 |
| Control boundary (input) | #808080 | sidebar | 3.67 | 3 |

### Proposed dark (24 pairs, all PASS)

| Element | Ink | Worst surface | Ratio | Need |
|---|---|---|---|---|
| Text primary | #F5F5F5 | hover #2B2B2B | 12.94 | 4.5 |
| Text secondary | #C4C4C4 | hover | 8.08 | 4.5 |
| Text tertiary | #9E9E9E | hover | 5.28 | 4.5 |
| Chip OK | #52CD86 | hover | 5.53 | 4.5 |
| Chip DENY | #FF8987 | hover | 5.00 | 4.5 |
| Chip ALLOW | #85B6E9 | hover | 5.24 | 4.5 |
| Chip REPORTED / UNKNOWN / PENDING | #FEC766 | hover | 7.11 | 4.5 |
| Chip pending (text) | #C4C4C4 | hover | 6.17 | 4.5 |
| Source user | #66CFE1 | hover | 6.04 | 4.5 |
| Source assistant | #C9ACFF | hover | 5.67 | 4.5 |
| Source tool | #FEC766 | hover | 6.84 | 4.5 |
| Source result | #A9B9CA | hover | 5.52 | 4.5 |
| Source decision | #F0CE65 | hover | 6.90 | 4.5 |
| Selection text | #F5F5F5 on #484848 | selection | 8.44 | 4.5 |
| Primary button label | #161616 on #F2F2F2 | fill | 16.12 | 4.5 |
| Dot OK | #52CD86 | hover | 6.99 | 3 |
| Dot DENY | #FF8987 | hover | 6.19 | 3 |
| Dot ALLOW | #85B6E9 | hover | 6.61 | 3 |
| Pending hollow ring | #A4A4A4 | hover | 5.69 | 3 |
| REPORTED dashed | #EDB345 | hover | 7.45 | 3 |
| UNKNOWN dotted | #EDB345 | hover | 7.45 | 3 |
| Idle solid dot | #868686 | hover | 3.88 | 3 |
| Focus ring | #E8E8E8 | hover | 11.48 | 3 |
| Control boundary | #808080 | raised #202020 | 4.11 | 3 |

### Today's tbound dark, same method [calc]

| Element | Ink | On | Ratio | Result |
|---|---|---|---|---|
| Text foreground | #FAFAFA | bubble | 14.48 | PASS |
| Text muted | #A1A1A1 | bubble | 5.83 | PASS |
| Chip OK | #71DF9C | raised | 9.01 | PASS |
| **Chip DENY** | #FB2C36 | raised | **4.00** | **FAIL** |
| Chip ALLOW | #60AAF3 | raised | 6.00 | PASS |
| Chip REPORTED | #FFCC69 | raised | 9.54 | PASS |
| Reported dashed border | #EDB333 | raised | 9.47 | PASS |
| **Control boundary (15% white)** | #2F2F2F effective | canvas | **1.47** | **FAIL** |

### Upstream values, same method [calc]: do not copy these
- Light tertiary text `#81858C` on white: **3.71:1** (fails 4.5).
- Light caption `#ADB2B8` on white: **2.13:1** (decorative only, but fails).
- Light idle `#D4D4D4` on white: **1.48:1** (fails 3:1).
- Light control border `rgba(0,0,0,.10)` on white, the composer stroke: **1.25:1** (fails 3:1, `InputBar.module.css:60`).
- Dark tertiary `#ADB2B8` on `#151517`: 8.54 (passes). Dark focus `#7AAAFF` on `#151517`: 7.83 (passes, but brand).

**Fix in the proposal:** each of these is replaced by a value that passes, as listed in section 5.

---

## 7. Proposed `tokens.css` skeleton

Tier 1 static (per theme) → Tier 2 alias (once) → Tier 3 component and geometry (theme-free). Plain static CSS. No `@import`, no `url()`, no inline style, no runtime injection. Compatible with `style-src 'self'`. Fonts are system stacks, so there is no `@font-face`.

Theme selection: light is the default. Dark applies when the OS asks for it, or when `data-theme="dark"` is set. `data-theme="light"` forces light. Setting the attribute is done by an external script (`script-src 'self'`), not inline. The dark static block appears twice because the OS query cannot be merged with an attribute selector. Keep the two blocks identical (a `diff` in CI is enough).

```css
/* tokens.css (proposal). Static CSS only. CSP: style-src 'self'. */

/* ── Tier 1: static ramp, per theme. Components never read these directly. ── */
:root,
:root[data-theme="light"] {
  color-scheme: light;
  --tb-s-canvas: oklch(1 0 0);
  --tb-s-raised: oklch(1 0 0);
  --tb-s-sidebar: oklch(0.975 0 0);
  --tb-s-bubble: oklch(0.955 0 0);
  --tb-s-hover: oklch(0.945 0 0);
  --tb-s-ink: oklch(0.2 0 0);
  --tb-s-ink-2: oklch(0.42 0 0);
  --tb-s-ink-3: oklch(0.5 0 0);
  --tb-s-on-ink: oklch(0.985 0 0);
  --tb-s-line: oklch(0.2 0 0 / 10%);
  --tb-s-edge: oklch(0.6 0 0);
  --tb-s-primary: oklch(0.2 0 0);
  --tb-s-on-primary: oklch(0.985 0 0);
  --tb-s-focus: oklch(0.2 0 0);
  --tb-s-selection: oklch(0.88 0 0);
  --tb-s-ok-line: oklch(0.52 0.13 150);
  --tb-s-ok-ink: oklch(0.44 0.12 150);
  --tb-s-deny-line: oklch(0.52 0.19 27);
  --tb-s-deny-ink: oklch(0.46 0.18 27);
  --tb-s-allow: oklch(0.44 0.09 250);
  --tb-s-warn-line: oklch(0.6 0.15 70);
  --tb-s-warn-ink: oklch(0.45 0.11 70);
  --tb-s-pending: oklch(0.5 0 0);
  --tb-s-idle: oklch(0.55 0 0);
  --tb-s-src-user: oklch(0.42 0.09 210);
  --tb-s-src-assistant: oklch(0.5 0.13 300);
  --tb-s-src-decision: oklch(0.45 0.11 92);
  --tb-s-src-result: oklch(0.42 0.03 250);
  --tb-s-scroll: oklch(0.85 0 0);
  --tb-s-shadow-panel: 0 0 0 0.5px var(--tb-s-line), 0 3px 8px rgb(0 0 0 / 3%), 0 0 16px rgb(0 0 0 / 2%);
}

@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    color-scheme: dark;
    --tb-s-canvas: oklch(0.18 0 0);
    --tb-s-raised: oklch(0.245 0 0);
    --tb-s-sidebar: oklch(0.215 0 0);
    --tb-s-bubble: oklch(0.275 0 0);
    --tb-s-hover: oklch(0.29 0 0);
    --tb-s-ink: oklch(0.97 0 0);
    --tb-s-ink-2: oklch(0.82 0 0);
    --tb-s-ink-3: oklch(0.7 0 0);
    --tb-s-on-ink: oklch(0.2 0 0);
    --tb-s-line: oklch(1 0 0 / 12%);
    --tb-s-edge: oklch(0.6 0 0);
    --tb-s-primary: oklch(0.96 0 0);
    --tb-s-on-primary: oklch(0.2 0 0);
    --tb-s-focus: oklch(0.93 0 0);
    --tb-s-selection: oklch(0.4 0 0);
    --tb-s-ok-line: oklch(0.76 0.15 155);
    --tb-s-ok-ink: oklch(0.76 0.15 155);
    --tb-s-deny-line: oklch(0.76 0.16 22);
    --tb-s-deny-ink: oklch(0.76 0.16 22);
    --tb-s-allow: oklch(0.76 0.09 250);
    --tb-s-warn-line: oklch(0.8 0.14 80);
    --tb-s-warn-ink: oklch(0.86 0.13 80);
    --tb-s-pending: oklch(0.72 0 0);
    --tb-s-idle: oklch(0.62 0 0);
    --tb-s-src-user: oklch(0.8 0.1 210);
    --tb-s-src-assistant: oklch(0.8 0.12 300);
    --tb-s-src-decision: oklch(0.86 0.13 92);
    --tb-s-src-result: oklch(0.78 0.03 250);
    --tb-s-scroll: oklch(0.4 0 0);
    --tb-s-shadow-panel: 0 0 0 0.5px var(--tb-s-line), 0 8px 24px rgb(0 0 0 / 40%);
  }
}

:root[data-theme="dark"] {
  color-scheme: dark;
  /* identical to the @media block above */
  --tb-s-canvas: oklch(0.18 0 0);
  --tb-s-raised: oklch(0.245 0 0);
  --tb-s-sidebar: oklch(0.215 0 0);
  --tb-s-bubble: oklch(0.275 0 0);
  --tb-s-hover: oklch(0.29 0 0);
  --tb-s-ink: oklch(0.97 0 0);
  --tb-s-ink-2: oklch(0.82 0 0);
  --tb-s-ink-3: oklch(0.7 0 0);
  --tb-s-on-ink: oklch(0.2 0 0);
  --tb-s-line: oklch(1 0 0 / 12%);
  --tb-s-edge: oklch(0.6 0 0);
  --tb-s-primary: oklch(0.96 0 0);
  --tb-s-on-primary: oklch(0.2 0 0);
  --tb-s-focus: oklch(0.93 0 0);
  --tb-s-selection: oklch(0.4 0 0);
  --tb-s-ok-line: oklch(0.76 0.15 155);
  --tb-s-ok-ink: oklch(0.76 0.15 155);
  --tb-s-deny-line: oklch(0.76 0.16 22);
  --tb-s-deny-ink: oklch(0.76 0.16 22);
  --tb-s-allow: oklch(0.76 0.09 250);
  --tb-s-warn-line: oklch(0.8 0.14 80);
  --tb-s-warn-ink: oklch(0.86 0.13 80);
  --tb-s-pending: oklch(0.72 0 0);
  --tb-s-idle: oklch(0.62 0 0);
  --tb-s-src-user: oklch(0.8 0.1 210);
  --tb-s-src-assistant: oklch(0.8 0.12 300);
  --tb-s-src-decision: oklch(0.86 0.13 92);
  --tb-s-src-result: oklch(0.78 0.03 250);
  --tb-s-scroll: oklch(0.4 0 0);
  --tb-s-shadow-panel: 0 0 0 0.5px var(--tb-s-line), 0 8px 24px rgb(0 0 0 / 40%);
}

/* ── Tier 2: alias. Declared once; values come from the active static set. ── */
:root {
  --background: var(--tb-s-canvas);
  --foreground: var(--tb-s-ink);
  --card: var(--tb-s-raised);
  --card-foreground: var(--tb-s-ink);
  --popover: var(--tb-s-raised);
  --popover-foreground: var(--tb-s-ink);
  --secondary: var(--tb-s-bubble);
  --secondary-foreground: var(--tb-s-ink);
  --muted: var(--tb-s-bubble);
  --muted-foreground: var(--tb-s-ink-2);
  --accent: var(--tb-s-hover);
  --accent-foreground: var(--tb-s-ink);
  --primary: var(--tb-s-primary);
  --primary-foreground: var(--tb-s-on-primary);
  --border: var(--tb-s-line);
  --input: var(--tb-s-edge);
  --ring: var(--tb-s-focus);
  --selection: var(--tb-s-selection);
  --sidebar: var(--tb-s-sidebar);
  --sidebar-foreground: var(--tb-s-ink);
  --sidebar-accent: var(--tb-s-hover);
  --sidebar-border: var(--tb-s-line);
  --shadow-panel: var(--tb-s-shadow-panel);
  --scrollbar-thumb: var(--tb-s-scroll);

  --info: var(--tb-s-allow);
  --destructive: var(--tb-s-deny-line);
  --destructive-foreground: var(--tb-s-deny-ink);
  --success: var(--tb-s-ok-line);
  --success-foreground: var(--tb-s-ok-ink);
  --warning: var(--tb-s-warn-line);
  --warning-foreground: var(--tb-s-warn-ink);

  /* state aliases: chips, dots and segments read only these */
  --state-ok: var(--success);
  --state-ok-ink: var(--success-foreground);
  --state-allow: var(--tb-s-allow);
  --state-deny: var(--destructive);
  --state-deny-ink: var(--destructive-foreground);
  --state-reported: var(--warning);
  --state-reported-ink: var(--warning-foreground);
  --state-pending: var(--warning);
  --state-pending-ink: var(--warning-foreground);
  --state-pending-mark: var(--tb-s-pending);
  --state-unknown: var(--warning);
  --state-idle: var(--tb-s-idle);
  --state-muted: var(--muted-foreground);

  /* source aliases: record provenance chips */
  --source-system: var(--tb-s-allow);
  --source-user: var(--tb-s-src-user);
  --source-assistant: var(--tb-s-src-assistant);
  --source-tool: var(--warning-foreground);
  --source-decision: var(--tb-s-src-decision);
  --source-result: var(--tb-s-src-result);
  --source-message: var(--tb-s-src-result);
}

/* ── Tier 3: component and geometry tokens. Theme-free. ── */
:root {
  --radius-xs: 4px;
  --radius-sm: 8px;
  --radius-md: 12px;
  --radius-lg: 16px;
  --radius-xl: 20px;
  --radius-panel: 28px;
  --radius: var(--radius-md); /* compat with current consumers */

  --space-1: 4px;
  --space-1h: 6px;
  --space-2: 8px;
  --space-2h: 10px;
  --space-3: 12px;
  --space-4: 16px;
  --space-5: 24px;

  /* No bare `monospace` tail: Windows CJK otherwise falls back to SimSun. */
  --font-sans: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC",
    "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Helvetica, Arial, sans-serif;
  --font-mono: ui-monospace, "SF Mono", Menlo, Consolas, "Liberation Mono",
    "Microsoft YaHei", "PingFang SC";
  --text-body: 14px;   --lh-body: 22px;
  --text-sm: 13px;     --lh-sm: 20px;
  --text-xs: 12px;     --lh-xs: 18px;
  --text-2xs: 11px;    --lh-2xs: 14px;

  --ease-std: cubic-bezier(0.4, 0, 0.2, 1);
  --dur-fast: 100ms;
  --dur-base: 200ms;
  --dur-slow: 300ms;

  --z-raised: 1;
  --z-sticky: 4;
  --z-overlay: 20;
  --z-popover: 100;
  --z-modal: 1000;

  --focus-width: 2px;
  --row-pad-x: 10px;
  --rail-width: 15rem;
  --inspector-width: 21.5rem;
  --row-h-turn: 33px;
  --row-h-cell: 38px;
  --row-h-toolbar: 32px;
  --row-h-button: 36px;
  --row-h-input: 32px;
}

/* Focus: 2 px ring in the neutral ink colour, offset outside the control. */
:focus-visible {
  outline: var(--focus-width) solid var(--ring);
  outline-offset: 2px;
}

/* Dots: shape carries meaning as well as colour. */
.dot { width: 0.5rem; height: 0.5rem; border-radius: 50%; border: 1px solid transparent; flex: none; }
.dot-ok { background: var(--state-ok); }
.dot-deny { background: var(--state-deny); border-radius: 2px; } /* square: not colour-only vs OK */
.dot-allow { background: transparent; border-color: var(--state-allow); box-shadow: inset 0 0 0 1px var(--state-allow); }
.dot-pending { background: transparent; border-color: var(--state-pending-mark); }
.dot-reported { background: transparent; border-color: var(--state-reported); border-style: dashed; }
.dot-unknown { background: transparent; border-color: var(--state-unknown); border-style: dotted; }
.dot-idle { background: var(--state-idle); }

/* Chips: state colour at 12% tint, ink for text. Radius xs. */
.chip-ok, .chip-allow, .chip-deny, .chip-reported, .chip-pending {
  border-radius: var(--radius-xs);
  border: 1px solid color-mix(in oklch, currentColor 40%, transparent);
  font: 500 var(--text-xs) / var(--lh-xs) var(--font-sans);
}

/* Scroll: thin and neutral. Standard path only. */
* { scrollbar-width: thin; scrollbar-color: var(--scrollbar-thumb) transparent; }

/* Reduced motion: kill transitions and animations (already present as styles.css:1526). */
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { transition: none !important; animation: none !important; }
}
```

Notes on the skeleton:
- `--tb-s-*` are static and never used by components. Components read `--background`, `--state-*`, `--source-*`, `--radius-*`, `--space-*`.
- `color-mix(in oklch, currentColor 40%, transparent)` is already used by tbound. Keep it.
- `--shadow-panel` and `--scrollbar-thumb` are new. Their contrast is not checked, since neither is a required 3:1 target by itself [inf].
- If the browser floor allows, `light-dark()` would remove the duplicate dark block. It needs a current engine [gen].

---

## 8. Migration checklist (for the rewrite)

1. Split `:root` in `styles.css` (lines 12–97) into the three tiers above. Keep every existing token name.
2. Replace `--input: oklch(1 0 0 / 15%)` with a solid edge token. This fixes the 1.47:1 boundary.
3. Set `--destructive-foreground` to the deny ink value. This fixes the 4.00:1 chip.
4. Change `--state-idle` from a 55% fill to a solid grey.
5. `index.html:6`: `color-scheme` becomes `light dark`. Add a theme setter in an external script. No inline script or `style=`.
6. Chips: drop uppercase and the 10 px mono, or keep mono only for IDs, paths and timestamps. This is a taste call. The current mono uppercase is part of the governance look, so ask before changing it.
7. Keep the reduced-motion block and `:focus-visible`. Only the ring colour changes.
8. Re-run `contrast.mjs` (or port its pairs to a test) after any palette edit.

---

## 9. Typography and font recommendation

- **Recommendation: system UI stack, no webfont.** Zero bytes, no licence duty, no CSP change. It is also what DeepSeek's ordinary UI does [src: `ui-theme/README.md:84`]. The stack in section 7 is the same generic list, minus their brand-only entries.
- tbound today names Inter and Geist Mono but ships neither (`@font-face` count is zero) [src]. So it already renders in the system fallback unless the user has Inter installed. The proposal makes that explicit.
- **If a webfont is wanted:** Inter, SIL OFL 1.1 [gen]. Serve the woff2 from `extensions/webchat/internal/webchat/assets/` with its OFL text beside it, `font-display: swap`, same origin only (`font-src 'self'` already permits this). Do **not** use Montserrat (their brand heading face, section 1).
- Mono: system mono stack (section 7). If bundling, JetBrains Mono (SIL OFL 1.1) [gen].

---

## 10. Verified vs inferred vs not verified

**Verified in source [src]**: all file:line citations above; the brand and MIT wording; the 8 sheet order; the upstream token values; the tbound token values and lines; the CSP; the absence of inline styles, `@font-face`, and `prefers-color-scheme` in tbound.

**Computed [calc]**: every ratio. The script is `contrast.mjs` and the output is `contrast-output.txt`, both in this folder. The upstream spot-checks (`upstream-check.mjs`) were not kept in the tree.

**Inferred [inf]**: the mood words; "assistant text is unboxed" (no class exists); the deny-dot square suggestion; the 0.5 px hairline recommendation; the choice of hue shifts for amber and allow.

**General knowledge [gen]**: Inter and JetBrains Mono licences (OFL 1.1); browser support for `light-dark()` and `corner-shape`.

**Not verified:**
- No browser render. Nothing was run, not upstream, not tbound. The contrast ratios are maths, not pixels.
- The upstream clone was read only. Two PNG screenshots were viewed. No runtime look was captured.
- tbound's row heights and paddings were not measured in the browser.
- The tbound worktree was being moved by another agent when it was read. The move has since landed (`18f71e6`). The read was of the file state at read time, which may have changed.
- No tbound tests were run (no code was changed).
