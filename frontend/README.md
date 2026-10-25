# Native frontend

The frontend is Go, with a small repository-owned cgo adapter in `bridge.c`.
SDL owns windowing, keyboard/text input, mouse events and rendering; SDL_ttf
renders Unicode text using the embedded DejaVu Sans font. There is one frontend
and no local HTTP server, webview, browser opener or webpage renderer.

Pinned, locally validated dependencies:

| Dependency | Version | Purpose |
|---|---|---|
| SDL3 (`sdl3.pc`) | 3.4.18 | Native window, input and renderer |
| SDL3_ttf (`sdl3-ttf.pc`) | 3.2.2 | Font rendering |
| DejaVu Sans | 2.37 | Embedded font; licence in `assets/FONT-LICENSE.txt` |
| Go | 1.27.x | Build, networking and application model |

`make deps` checks the exact SDL versions before building. Install the matching
development libraries, a C compiler and pkg-config. There are no third-party Go
modules. Runtime installations must include the matching shared SDL libraries
and their normal transitive dependencies. Deliberate dependency upgrades should
update this table, the Makefile, and rerun all frontend checks.

Build from the repository root with `make build`. SDL calls stay on the main OS
thread. Network jobs return completions to the UI thread; configuration changes
and disconnects invalidate stale completions. Closing a request tab cancels its
network job. Browser mode negotiates the node tab limit (four by default); legacy metadata
mode retains seven request tabs and one reserved Settings slot.
each request tab retains at most fifty metadata history entries in memory.

Use Ctrl/Cmd+T for a new tab, Ctrl/Cmd+W to close it, Ctrl/Cmd+L for navigation,
Ctrl/Cmd+, for Settings, Ctrl/Cmd+R to reload and Ctrl/Cmd+Q to quit. Text fields
support Unicode input, arrows, Home/End, selection with Ctrl/Cmd+A, paste,
Backspace/Delete, and Tab/Shift+Tab focus traversal. Search queries first display
their resolved URL; a second Go submits it. Back/Forward navigate remote page history in browser mode and redisplay local
metadata in explicit `-metadata` mode.

`go test ./frontend` exercises the SDL event loop with its dummy video driver,
as well as the UI model and a local TLS node. `make smoke` tests the built client.
For a desktop check run `build/wolfclient -state-dir /a/private/test/directory
-smoke-test -screenshot /tmp/wolftunnel.bmp` without the dummy driver. This opens
a real native window briefly, injects text/tab/settings events and exits.

The window chooses a size within 84% of the usable display bounds, capped at a
preferred 1180×760 content size adjusted for display scale. High pixel density
is enabled. Text rasterises at the actual output scale and maps texture pixels
directly to output pixels; resizing no longer enlarges a fixed 1200×860 canvas.
Window size, output pixels and scale changes appear in client diagnostics.
The layout responds to width, scrolls overflowing content and pages tab strips.
Legacy request summaries show the result and a few metrics; technical fields are
collapsed. Settings defaults to one masked invitation, with manual setup and
diagnostics available separately. Pending approval input survives Apply and
Settings navigation. Connect applies the visible fields and saves credentials.

`-window-size 1100x700` and `-ui-scale 1.5` provide explicit display overrides;
automatic display DPI is the default. `-smoke-view settings` or `manual` captures
those layouts when combined with `-smoke-test -screenshot /tmp/wolf.bmp`.

Known UI limits: fields use a simple caret editor rather than a full
accessibility/text-editing toolkit. IME
composition overlays, screen readers, touch input and platform packaging need
additional work. Browser viewports contain remotely decoded pixels; only `-metadata` uses request
summaries. Browser input preserves physical keys separately from committed text.
The client shares no local clipboard/profile with the page.

SDL references: [window and renderer](https://wiki.libsdl.org/SDL3/SDL_CreateWindowAndRenderer),
[text input](https://wiki.libsdl.org/SDL3/SDL_StartTextInput),
[SDL_ttf text](https://wiki.libsdl.org/SDL3_ttf/TTF_RenderText_Blended).

Browser validation: `make browser-smoke BROWSER=/path/to/chromium/engine` runs a
temporary local TLS node, sandboxed engine, native dummy-driver screenshot and
binary capture smoke. `make browser-native` captures the actual desktop window.
Browser screenshot smoke has a 25-second deadline and its test a 40-second parent
deadline. Source node JPEG and native BMP are saved separately in `build/v0.5/`.
The viewport texture is clipped below trusted native controls; coordinate mapping
accounts for letterboxing/DPR 1, and resize notifications coalesce over 100 ms.
See the [v0.5 report](../docs/USABILITY-2026_10_25.md).

New client defaults use `~/.local/share/wolftunnel/client` and the stable diagnostic
path `~/.local/share/wolftunnel/logs/client.log`. First launch opens Settings;
full invitations and bare codes are distinguished, and pasted host:port addresses
are normalised. A bare code requires an independently verified pin. Successful
connection returns to a page tab. `-open URL` opens a native page after connecting;
with explicit `-screenshot` it captures and exits under a timeout. The separate
`-interaction-smoke` flag adds deterministic mouse/physical-key input for testing.
