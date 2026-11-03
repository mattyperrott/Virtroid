# Shield transaction card: design brief

Built in Shield 0.2.0 (`src/main/java/io/virtroid/shield/ui/card`), without
Swap (section 9).

Design the complete interactive UI and every user-flow screen for Shield's
transaction card: where a user shields, withdraws and privately sends their
own crypto through 0xbow's Privacy Pools v2. Shield runs on Virtroid's cloud
phone (Android 16, Launcher3). The user's wallet stays on their own device and
connects over WalletConnect; Shield never holds the wallet's keys.

Use the latest attached screenshot as the primary layout reference, and the
earlier screenshots for each tab's fields. Where this brief and the
screenshots disagree, follow this brief: the screenshots come from another
app, with features Shield doesn't have.

## 1. Two surfaces: the widget and the card

An Android home-screen widget can't take typing, can't change its own size,
and sits on the most-looked-at screen on the phone. So the flows don't run
inside the widget; they run in a card that opens from it.

**The widget** (full width, 5 × 2 cells):

- Shield's mark and name, and the selected chain as a chip.
- "Private balance": the shielded balance only if the user turned it on in
  Shield; otherwise "Hidden".
- Under it, only when deposits need looking at: "1 deposit waiting for
  screening" in amber, "1 deposit declined" in red.
- Three buttons with icons: Shield (lime, the primary one), Withdraw, Send.
  Each opens the card on that tab. Tapping anywhere else opens the card on
  its default tab. Before set-up there is one button, Set up.
- Resized to one row, it keeps the buttons under its name, with what needs
  looking at, or else the balance, on the right. Narrower than the home
  screen, the buttons drop their icons.
- Nothing is typed, signed or sent on the widget itself.

**The card** is a floating window over the home screen, not a full-screen
page:

- It opens out of the widget with Android's widget launch animation, so it
  reads as the widget expanding. The home screen stays visible, dimmed,
  behind it.
- It stays above the keyboard while the keyboard is open.
- Back on the first step, a tap outside it, or Done closes it back to the
  home screen, and the widget shows the new state.
- The same card is Shield's in-app transaction screen, so the widget and the
  app share one flow.

## 2. Layout: match the latest screenshot

- A full-width rounded outer container.
- Three tabs in a horizontal pill-shaped bar: Shield, Withdraw, Send.
- A rounded inset content panel underneath.
- In the first step, the action label at the upper left and the token/chain
  selector at the upper right.
- A large editable amount below the label.
- A circular right-arrow button to the right of the amount.

Match the screenshot's generous spacing, rounded shapes, large amount
typography and clear alignment. Vertically centre the token selector against
the action label, and the circular Continue button against the large amount.

Keep the first step as minimal as the screenshot, with one exception: a
single line of muted text under the amount saying how much this action can
move ("Up to 1,250.00 USDC in one send"). Users need that before they type.
Fees, estimates and details wait for later steps, or appear when validation
needs them.

Keep the outer container and tabs visible throughout. Replace only the
panel's content between steps, and let the panel's height change smoothly
when a step needs more room.

Remove step numbers, progress dots, decorative shield icons, and the Request
and Swap tabs (Swap: section 9).

## 3. Colours and type

Use the screenshot's layout with Virtroid's palette:

- Outer background: #131313
- Inset content panel: #1B1B1B
- Tab bar, token selectors and secondary controls: #242424
- Primary lime accent: #AFD135
- Pressed accent: #D0EC62
- Text and icons on lime: #0E0E0E
- Primary text and entered amounts: #F4F4F5
- Secondary text: #A1A1AA
- Muted text and the empty-amount placeholder: #8A8A93
- Error: #DF7E79
- Warning: #F5A623
- Subtle borders: white at about 8% opacity

The selected tab gets the screenshot's rounded outline, in lime, with
off-white text; inactive tabs get muted text. The circular Continue button is
lime with a dark right arrow. Use lime for primary confirmation buttons,
focused controls and valid-input feedback.

The widget uses the same palette, so nothing changes colour as the card opens
out of it.

Use the system font in Material 3's type scale; no custom fonts (a widget
can't use them, and the card should match the widget). Flat, solid fills with
minimal elevation. Keep recognisable token logos and chain badges. Follow
Material 3 interaction states and accessibility, with touch targets of at
least 48 dp.

## 4. Shared behaviour

- **Chains:** Arbitrum (the default), Base, Optimism and Ethereum. Each chain
  is a separate account with its own balances. The selector picks the token
  and chain together and shows the chain as a badge. Every label that names a
  chain uses the selected one ("Into the USDC pool on Arbitrum").
- **Tokens:** ETH, USDC and USDT on each chain. Shielded ETH, USDC and USDT
  are held as ppETH, ppUSDC and ppUSDT, which earn Aave v3 interest. The
  exception is USDT on Base, held as plain USDT, which earns nothing. Show
  amounts in the underlying token (USDC), and name the pp token only in
  review details.
- **Where it opens:** on the tab of the widget button that was tapped.
  Otherwise on Send if anything is spendable, else on Shield. The prototype's
  first view is Send's amount step.
- **Drafts:** each tab keeps its own draft through Back, tab switches and
  errors.
- **Continue:** the circular arrow, labelled "Continue" for accessibility. It
  advances only when the step is valid; when it can't, the reason shows as
  text under the amount.
- **Motion:** forward is a subtle horizontal slide and fade of 200–300 ms;
  Back reverses it. Keep it inside the panel. With reduced motion, crossfade
  only. Screen readers announce each new step's title.
- **Back:** a small Back control on every step after the first.
- **Reviewing versus authorising:** labelled buttons keep these apart:
  "Review send" opens the review, "Confirm send" sends.
- **No double submissions:** after Confirm, the button is replaced by the
  processing step and can't be pressed again.
- **Success** shows only after the outcome is confirmed on chain.

## 5. How Shield works: what the screens must reflect

- **Screening.** 0xbow screens each new deposit before it can be sent or
  withdrawn privately. Until then it is "waiting for screening"; don't
  promise a time. A declined deposit can't be sent privately. Its only way
  out is an exit: back, publicly, to the address that deposited it, with the
  wallet paying the network fee.
- **One deposit per transaction.** A send or withdrawal spends from one
  deposit (and whatever is left of it) at a time. So the most one
  transaction can move is the largest single deposit, not the total. When
  the total is more, the amount step says so: "You can move up to 1,250.00
  USDC in one send. Your 2,000.00 USDC is in separate deposits."
- **Who signs what.** The wallet is needed to connect, to sign the account
  message (twice), to publish the account once per chain, to approve a
  token, to shield, and to exit a declined deposit. Withdrawals and sends
  need no wallet prompt: Shield proves them with the account's own keys, and
  0xbow's relayer sends them and pays the network fee. So for Withdraw and
  Send, Confirm is the authorisation. The review says so: "This can't be
  undone."
- **The relayer's fee** is a flat fee on top. The recipient gets exactly the
  amount entered, and the shielded balance drops by the amount plus the fee.
  "Max" moves the largest deposit, less the fee. The fee is quoted with an
  expiry.
- **The proof.** Withdraw and Send make a zero-knowledge proof on the phone
  before review, which takes a noticeable moment ("Making the proof…"). The
  review shows the fee the relayer committed to, with a quiet countdown to
  its expiry. If it expires, "Refresh" makes a new proof and quote before
  Confirm works again.

## 6. Shield

**SH1 — Amount**

- Label: "Amount to shield".
- Token/chain selector at the upper right, defaulting to USDC on Arbitrum.
- Large editable amount, initially 0, and the circular Continue button.
- Muted line: "In your wallet: 820.00 USDC".
- Validate against the connected wallet's balance. For ETH, leave room for
  the network fee.

**SH2 — Destination**

- "You shield", with the amount and token.
- "Into the USDC pool on Arbitrum", for the selected token and chain.
- The amount you'll hold: "About 100.00 USDC, held as ppUSDC".
- "Shielding to your own account", with the connected wallet's address,
  shortened.
- "Earns Aave v3 interest while shielded". Show a rate only if it is read
  live, never a fixed or stale number. Leave the line out for USDT on Base.
- "0xbow screens each deposit before it can be sent privately."
- "Your wallet shows the network fee."
- Button: "Review shield".

**SH3 — Review shield**

- The amount, the wallet it comes from, the chain, the pool, and what you'll
  hold.
- How many wallet prompts are coming: "Your wallet will ask twice: to let
  the pool use 100.00 USDC, then to shield it."
- Button: "Confirm shield".

**SH4 — Token approval** (when the token needs it; ETH doesn't)

- "Approve 100.00 USDC in your wallet": the token, the exact amount (never
  unlimited) and what is being allowed to spend it.
- "Open wallet" when the wallet app is on this phone. Otherwise "Approve it
  in your wallet app" while waiting.
- Then "USDC approved", with a lime check, and on to the shield's own wallet
  prompt.
- The approval and the shield are separate wallet prompts.

**SH5 — Processing**

One status at a time, with a restrained indicator: "Waiting for your wallet",
then "Confirming on Arbitrum", then "Updating your balance".

**SH6 — Done**

- "Shielded": "100.00 USDC is in the USDC pool on Arbitrum. It can be sent
  once 0xbow's screening approves it."
- Don't show it as spendable. Show the transaction hash, copyable.
- Buttons: "Done" and "Shield again".

## 7. Withdraw

**W1 — Amount**

- Label: "Amount to withdraw".
- Token/chain selector at the upper right.
- Large editable amount, and the circular Continue button.
- Muted line: "Up to 1,250.00 USDC in one withdrawal", with a small "Max".
- Validate against what one withdrawal can move.

**W2 — Public recipient**

- "Recipient receives": the exact amount and token.
- For pp tokens: "You receive USDC, redeemed from ppUSDC on the way out."
- "Received publicly on Arbitrum", for the selected chain.
- An outlined field labelled "Recipient 0x address", with Paste.
- Supporting text: "Anyone can see what arrives at this address. Use a fresh
  address that isn't linked to you."
- If the address is the connected wallet, a warning: "This is the wallet you
  shield from. Withdrawing here links the two."
- "The relayer's fee is added on top of the amount you entered."
- No gas-token option and no exact-amount switch: the amount is always
  exact.
- "Review withdrawal" appears once the address is valid. Pressing it makes
  the proof, then opens the review.

**W3 — Review withdrawal**

- Taken from your shielded balance: the amount plus the fee.
- Recipient receives: the amount and token.
- The full recipient address (checksummed, monospaced) and the chain.
- "Public: anyone can see this arrival."
- The relayer's fee, the total deducted, and the fee's expiry countdown.
- "This can't be undone."
- Button: "Confirm withdrawal".

**W4 — Processing**

"Sending through the relayer", then "Confirming on Arbitrum", then "Updating
your balance".

**W5 — Done**

- "Withdrawal complete": the amount received, the token, the recipient, the
  chain, and the transaction hash.
- Buttons: "Done" and "Withdraw again".

## 8. Send

**S1 — Amount**

- Reproduce the latest screenshot's arrangement.
- Label: "Amount to send".
- USDC/chain selector at the upper right.
- Large editable amount, and the circular Continue button on the amount row.
- Muted line: "Up to 1,250.00 USDC in one send", with a small "Max".
- Validate against what one send can move.

**S2 — Recipient**

- "Recipient receives": the exact amount, in the same token. Shield can't pay
  in a different token.
- "Sent privately on Arbitrum. They receive it in their own Privacy Pools
  account."
- An outlined field labelled "Recipient 0x address", with Paste.
- Check that the address has a Privacy Pools account on this chain:
  "Checking…", then "Can receive privately" with a lime check. If it has
  none: "This address hasn't opened Privacy Pools on Arbitrum, so it can't
  receive privately. They can open it in Shield or 0xbow's app, or you can
  withdraw to them publicly." The last part links to Withdraw, keeping the
  amount and address.
- "The relayer's fee is added on top of the amount you entered."
- "Review send" appears once the address is valid and can receive. Pressing
  it makes the proof, then opens the review.

**S3 — Review send**

- Taken from your shielded balance: the amount plus the fee.
- Recipient receives: the amount and token.
- The full recipient address and the chain.
- "Private: it stays in Privacy Pools, and they find it with their own
  wallet."
- The relayer's fee, the total deducted, and the fee's expiry countdown.
- "This can't be undone."
- Button: "Confirm send".

**S4 — Processing**

"Sending through the relayer", then "Confirming on Arbitrum", then "Updating
your balance".

**S5 — Done**

- "Sent": the amount, the recipient, the chain, and the transaction hash.
- "They'll see it when they open Privacy Pools with that wallet."
- Buttons: "Done" and "Send again".

## 9. Swap: design it, but it isn't in Shield yet

Shield has no swap. 0xbow's SDK has swap routes, but their quotes come from
an outside service (LI.FI), and adding them means Shield contacts a new
service: a separate decision. Design the Swap tab so it's ready; the card
ships with three tabs until then.

**SW1 — Amount**

- Label: "You swap".
- Source token/chain selector at the upper right.
- Large editable amount, and the circular Continue button.
- Muted line: "Up to … in one swap". Validate against it.

**SW2 — Receiving token**

- "You receive privately", with a destination-token selector initially set
  to USDT.
- The estimated output, with "Quoted at review, after fees."
- "Swapping to your own account", with the account.
- "Stays private", as plain supporting text.
- "Max slippage", initially 0.5%.
- Estimated fees.
- Refresh the quote when the amount, token or slippage changes. "Review swap"
  appears once a valid quote is in.

**SW3 — Review swap**

- The input, the quoted output and the minimum received.
- The rate, slippage, fees, chain and destination account.
- An expiry; refresh an expired quote before Confirm works.
- Button: "Confirm swap".

**SW4 — Processing** and **SW5 — Done**

- "Swap complete": the amounts, the destination token, and whether the
  output is spendable now or waiting for screening, as the route reports.
- Buttons: "Done" and "Swap again".

## 10. Setup and other states

**Setup**

- **Wallet not connected:** "Connect wallet". Show a QR code for a wallet on
  another device, and "Open wallet app" for one on this phone. Then waiting,
  connected, or declined.
- **Account not set up:** "Sign in your wallet", twice, explaining why: the
  signature makes the account's keys, so the same wallet always gets back
  in. If the two signatures differ: "Your wallet signed the same message two
  different ways, so this account's keys couldn't be made again later. Use a
  wallet with standard signatures."
- **Account not published on this chain** (before its first send or
  withdrawal): "Publish your account on Arbitrum": one wallet transaction,
  and the wallet pays the network fee.
- **Wallet on the wrong chain** (Shield only): "Switch your wallet to
  Arbitrum".

**Balances**

- **Syncing:** "Updating your balance…". The amount step's muted line
  appears once balances are in.
- **Nothing spendable yet:** on Withdraw and Send, "Nothing is ready to spend
  yet. 100.00 USDC is waiting for screening.", with a link to Shield.
- **Declined deposit:** a notice on the first step of every tab: "Screening
  declined 50.00 USDC. Take it back out".
  - It opens an exit review: "Goes back publicly to 0xab…cd, the address
    that deposited it. Your wallet sends it and pays the network fee."
  - Then "Exit", the wallet, confirming, and done.

**Amounts**

Empty, zero, not a number, too many decimals for the token, more than
available, more than one transaction can move, and smaller than the relayer's
fee ("The balance is smaller than the relayer's fee").

**Recipients**

Incomplete, invalid characters, a failed checksum (on mixed-case addresses),
or the zero address. Then checking, and valid ("Valid address", with a lime
check).

**Routes**

The token isn't offered on this chain.

**Quotes**

- Loading.
- Unavailable: "The relayer didn't quote. Try again."
- Changed: the fee went up, so show the old and new fees and ask again.
- Expired: "Refresh".

**Failures**

- **Wallet declined:** "Your wallet declined it." with Try again.
- **Transaction failed:** what happened, with the draft kept and Try again.
- **Still confirming after a long wait:** "Still confirming. You can close
  this; Shield keeps checking." Never offer to send again while one is in
  flight.
- **Network unreachable** (including a slow Tor connection): say so, and
  keep the draft.

Always pair validation colours with words.

## 11. Deliverables

- Every screen in sections 6–10, including the approval, setup, exit and
  error states, and Swap's screens from section 9.
- The widget in its states (not set up; balance hidden; balance shown;
  deposits waiting; deposit declined), and the opening animation from the
  widget into the card.
- A working interactive prototype with:
  - wired tabs, amount entry, the token/chain selector and Max
  - Back and Continue
  - recipient validation and the receive check
  - the proof step, and a review whose fee quote ticks down
  - confirmation, processing and results
- Demo data:
  - A funded account where every flow completes. It has one deposit waiting
    for screening, one declined, and two spendable deposits, so the
    one-transaction limit shows.
  - A zero-balance preview and a not-connected preview.
  - Every estimate and outcome in the prototype labelled "Simulated".
- First view: Send's amount step, matching the latest screenshot's
  composition in Virtroid's palette.
