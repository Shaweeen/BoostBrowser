# Sync toolbar click fix

## Requirement

The synchronization assistant receives one selected master environment ID and
the selected follower environment IDs when the user starts synchronization.
The implementation must use that live selection as the source of truth. It
must not assume a fixed number of environments or a fixed window size.

## Failure path fixed

The old native fallback converted a master toolbar point by the ratio of the
two **outer** window rectangles and posted the result as a follower **client**
coordinate. A shorter tiled follower therefore received a y-coordinate in a
different browser row; with the first wallet-icon click this could be a native
frame control. A persisted HWND could also be stale after a close/reopen.

The new path resolves the selected profile IDs through the live process tree at
sync start. Each follower is mapped independently from its current client
width, render-child top and DPI. Page points (including wheel fallback) use the
render child. Native toolbar points keep their row; left/right control clusters
keep their edge inset while the central omnibox follows the selected follower's
actual width. A native mouse-down captures the exact target HWND and point for
its matching mouse-up. A popup that cannot be matched is dropped rather than
sent to the browser underneath it.

Address typing is replayed through the focused native omnibox after its click;
the committed navigation is then matched by each follower's DevTools target.
Ctrl+wheel measures the master page's final CSS scale and converges each
follower to that scale instead of applying a relative wheel delta.

No data is deleted or rewritten in browser profiles, cookies, extension stores,
wallet stores or login state.

## Acceptance matrix

Run on Windows with the same installed extensions and wallet profiles in every
selected environment:

1. Open an arbitrary number of environments in the sync assistant, refresh the
   environment list, select one master and all intended followers, and start
   sync. Confirm the follower count equals the selection; no hard-coded count is
   involved.
2. Tile the selected environments so their actual client sizes differ from the
   master (including a narrow last row) and repeat the first click on a pinned
   wallet icon. Every selected follower must open its wallet surface; no browser
   frame may close, minimize, or navigate away.
3. Repeat at each available Windows display scale (100%, 125%, 150%) and with
   the master/followers on different monitors when available.
4. With a wallet popup open, click its nested menu, account choice, confirm,
   authorize and sign controls. The corresponding follower popup must receive
   the same operation. If a follower popup is not present, the click must be
   skipped rather than delivered to that follower's main browser frame.
5. Close and reopen one follower while sync remains active, refresh the sync
   list, and repeat the wallet-icon click. The reopened environment must use its
   new live window handle; no old handle may receive input.
6. Click the middle and right side of the master address bar, type a URL, edit
   it with selection/backspace, then press Enter. Each follower must show the
   same omnibox text while typing and navigate only to the committed URL.
7. With differently sized selected windows, use Ctrl+wheel up/down over a page.
   Followers must converge to the master page scale; repeat after a layout
   change without restarting synchronization.

The Mac test host can run the platform-neutral geometry and target-planning
tests, but it cannot prove Win32 hit-testing or actual wallet popup behavior.
