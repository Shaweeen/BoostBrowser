# BrowserStudio v1.7.137

## Ctrl+wheel absolute page zoom sync

- Ctrl+mouse-wheel zoom is debounced from the master window and aligned to the master's absolute page scale.
- Followers no longer apply a relative zoom guess; each follower converges to the master ratio even when windows started at different zoom levels.
- Zoom sync requests use a dedicated non-dropping worker path, so a busy mouse-input queue cannot leave followers at the old scale.
- Normal vertical/horizontal scrolling and page coordinate mapping remain unchanged.
