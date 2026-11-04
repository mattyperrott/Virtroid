# Shield: design

What the app is for: *a widget gives an address, what's sent to it becomes
private, and it comes back out or goes somewhere else.* Everything else stays
out of the way. The owner rejected the earlier flow (connect a wallet, sign
twice, register, approve, deposit) on 2026-11-04 as too hard and too slow.

## Widget

One row, the full width of the home screen, the same shape as Fauxx's: mark
(28dp), a 16sp title and 13sp detail, then one action. A tap anywhere else opens
the app. Amounts are hidden unless turned on in the menu.

| State | Title | Detail | Action |
|---|---|---|---|
| No wallet | Not set up | Open Shield to set up | Set up |
| Nothing in the pool | Deposit address | 0x12aB…9f3C | Copy |
| Below the minimum | 3 USDC received | Send more to reach 11.5 USDC | Copy, or Send out |
| Waiting on gas | 250 USDC received | Waiting for a lower fee | Copy, or Send out |
| Moving or being screened | Shielding 250 USDC | Usually about 10 minutes | spinner |
| In the pool | 250 USDC, or Private balance | Private balance, or Hidden | Send out |
| Declined | 1 deposit declined (red) | Tap to take it back | none |

## App

Compose, one window (`ui/Screens.kt`), the palette in `ui/Theme.kt` (dark, lime
for the primary action, the same on every device). A screen's content scrolls and
its action stays pinned below it, as the cloud phone's screen is short
(752x1600 at 340dpi).

- **Set-up**: create or restore. New words are shown once, with a box to tick
  before going on; nothing else is asked.
- **Home**: what's on its way, the private balance, the deposit address with
  Copy and its QR code, and Send out when there's something to send.
- **Send out**: address, amount (empty sends it all), then a review of what
  arrives and the relayer's fee, with a warning when it could be linked to its
  deposit.
- **Take it back**: only for a declined deposit, and it says the exit is public.
