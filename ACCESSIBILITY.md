# Accessibility

Last reviewed: 16 October 2026.

VirtRoid aims to make its client, documentation, and operator tools usable by
people with different access needs. Accessibility reports and improvements are
welcome. This statement describes the current implementation and assessment
limits; it does not claim WCAG conformance or a completed accessibility audit.

## Environments and scope

- **Android client:** Android 9 (API 28) or later. Native controls include text
  labels and content descriptions for many actions, with accessibility
  announcements for in-app notices. Their presence does not establish that
  every screen works with assistive technology.
- **Remote Android viewer:** a streamed video display with forwarded input.
  Its accessibility behavior differs from the phone's native client screens.
- **Operator console:** a browser interface. Screen-reader, keyboard, zoom,
  contrast, and mobile-browser behavior need to be assessed for the browser and
  version being used; there is no published assistive-technology support matrix.
- **Documentation:** Markdown rendered on GitHub, with text descriptions and
  alternative text for the README's architecture and lifecycle diagrams.

## Known limitations and unverified behavior

- The remote viewer does not forward the runtime apps' accessibility node tree
  to the phone. A screen reader on the phone cannot inspect those remote apps
  as if they were native controls. Text, focus, and other semantics inside the
  video are not exposed through that stream.
- Complete TalkBack and Switch Access flows, keyboard-only operation, large
  fonts, display scaling, contrast, and touch-target sizes have not been
  systematically verified across all client screens. Some controls are small
  and some labels have fixed line limits.
- The operator console has not had a documented, comprehensive accessibility
  audit. Focus order, chart alternatives, and dynamic status announcements may
  need improvements.
- Screen-capture protections can prevent screenshots or screen-sharing tools
  from showing protected content. Reports can describe a barrier in text; a
  screenshot is not required.
- Apps installed inside a runtime have their own accessibility characteristics.
  Installing an accessible app does not resolve the streamed viewer's lack of
  remote accessibility semantics.

## Report a barrier

Use the [accessibility issue form](https://github.com/mattyperrott/Virtroid/issues/new?template=accessibility.yml).
Include whichever details you can provide:

- The task you were trying to complete and the screen or control that blocked it.
- The client version or commit, Android version and device, or browser and OS.
- Assistive technology and version, such as TalkBack, Switch Access, or a screen
  reader; relevant font, display, zoom, or input settings.
- Steps to reproduce, what you expected, and what happened.
- A workaround or suggested improvement, if known.

You do not need to disclose a disability, medical information, account
credentials, or runtime contents. Public issues are visible to everyone. For
a barrier that exposes sensitive data or bypasses a security control, use the
[private security reporting channel](SECURITY.md) instead.

If you need to report an accessibility barrier privately, email
[email@mattyp.com.au](mailto:email@mattyp.com.au) with the subject
`VirtRoid accessibility report`.

The maintainer will assess the report and discuss practical improvements as
time permits. There is no guaranteed response or resolution deadline.

## Contribute an improvement

Follow [CONTRIBUTING.md](CONTRIBUTING.md). Prefer clear labels, meaningful focus
order, visible focus, usable touch targets, scalable text, and status information
that does not depend on color alone. Check the affected flow with the relevant
assistive technology and record what you tested. Accessibility work should
preserve authentication, privacy, and screen-capture controls.
