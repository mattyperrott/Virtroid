# USDT deposits

Users pay by sending USDT on the TRON network (TRC20) to a deposit address of
their own. Deposits are credited to a USDT balance that runtime use and
storage are paid from.

- Each account's address is the key at `m/44'/195'/0'/0/i` of one BIP39 seed,
  with `i` the account's deposit index (1, 2, ...). It is the path TronLink and
  most Tron wallets use, so the seed also opens every address there.
- `virtroidd` holds only the extended public key of that path
  (`TRON_DEPOSIT_XPUB`): it derives the address a user sees, keeps the USDT
  ledger, and credits each transfer once.
- `virtroid-payments` (the `payments` profile) holds the seed. It lists
  confirmed USDT transfers to the accounts' addresses through TronGrid, checks
  each one again on a second node (PublicNode by default) and reports it to
  `virtroidd`, signed with its own key (`PAYMENTS_PUBLIC_KEY`). It only watches
  addresses its own seed derives, so every address users see is one this
  wallet can spend. Addresses whose wallet was opened in the last day are
  checked every 30 seconds, others every 30 minutes.

Deposits are swept into a treasury and swapped into what running Virtroid
costs; see [Treasury](#treasury).

## Charging

A new account starts on a free trial: one runtime and an hour of running
time. virtroidd meters the time every minute while a runtime runs (booting
does not count). When the hour is used up the runtime stops, keeping its
data, and cannot start again until the account deposits.

The first deposit ends the trial and lifts its limits: up to 10 runtimes,
1,000 starts a day and 64 GiB of stored data. One runtime still runs at a
time, as each account's VM has room for one. From then on the account's use is
charged to its balance. Accounts the operator gave another entitlement source
are never charged.

- Runtime time: `RUNTIME_PRICE_PER_HOUR_MICRO` (millionths of a USDT, default
  100000, i.e. 0.10 USDT) per hour a runtime is running, charged every minute
  into one ledger entry per runtime and hour. A runtime keeps running after
  its viewer closes for the inactivity period its owner chose (30 minutes to a
  week) unless they stop it, and that time counts towards the trial hour and
  is charged too.
- Storage: `STORAGE_PRICE_PER_GIB_MONTH_MICRO` (default 50000, i.e. 0.05 USDT)
  per GiB of stored runtime data, local or on Sia, per 30 days, charged once a
  day.
- Starting a runtime needs ten minutes of runtime credit. When the balance
  reaches zero, the account's runtimes are stopped. Their stored data is kept
  and storage charges continue, so the balance can go below zero.

The defaults are placeholders: set both prices in `.env` before taking
deposits. The app's Wallet screen shows them.

## Treasury

The treasury is the seed's address at index 0 (accounts start at 1). Every
30 minutes it:

1. **Sweeps.** A deposit address holding at least `PAYMENTS_SWEEP_MIN_USDT`
   (20) USDT is swept into the treasury. Moving USDT costs TRX, so the
   treasury first sends the address 15 TRX, then the address sends its whole
   USDT balance. Each transaction is built and signed in this service; a
   Tron node only relays it.
2. **Swaps through SimpleSwap** (floating rate), sending only to addresses
   set in `.env`:
   - **TRX** for fees, when the treasury holds less than 50 TRX, up to 150.
   - **Siacoin** to `PAYMENTS_SIA_ADDRESS` (indexd's wallet), when indexd
     holds 10% or more below `PAYMENTS_SIA_TARGET_SC` (20,000). virtroidd
     reports indexd's balance; the swap goes nowhere unless the reported
     wallet is the pinned one.
   - **ETH** to `PAYMENTS_ALEPH_FUND_ADDRESS` (the provisioner's address),
     when its Aleph credits, plus the ETH already waiting there, cover less
     than 90% of `PAYMENTS_ALEPH_DAYS` (7) days of what
     `PAYMENTS_ALEPH_SPEND_ADDRESS` spent per day over the last week. The
     provisioner buys credits with that ETH (`PROVISIONER_CREDIT_BUY`),
     keeping 0.002 ETH for gas.

Each swap is at least `PAYMENTS_MIN_SWAP_USDT` (25) and at most
`PAYMENTS_MAX_SWAP_USDT` (200) USDT, and no more than
`PAYMENTS_MAX_DAILY_USDT` (500) USDT leaves for swaps in 24 hours. The cap
and the swaps still in flight are read from SimpleSwap's own record of this
key's exchanges, so a restart forgets neither. Each SimpleSwap swap costs
its spread and fees, and each sweep about 6 to 14 TRX.

**Modes.** `PAYMENTS_TREASURY` and `PROVISIONER_CREDIT_BUY` are `dry-run` by
default: they read balances and quotes and log what they would do
(`docker logs virtroid-payments`, `docker logs virtroid-provisioner`), and
move nothing. `live` signs and sends; `off` stops them.

**Before going live:**

1. Send the treasury some TRX (150 is enough to start); its address is the
   first line the treasury logs. Without TRX it cannot move USDT, and it
   says so.
2. Make the SimpleSwap key readable by the service and point to it:

   ```sh
   sudo chgrp virtroid-payments /etc/virtroid/secrets/simpleswap-api-key
   sudo chmod 0440 /etc/virtroid/secrets/simpleswap-api-key
   ```

   with `SIMPLESWAP_API_KEY_FILE=/etc/virtroid/secrets/simpleswap-api-key`
   in `.env`.
3. Pin the destinations in `.env`: `PAYMENTS_SIA_ADDRESS` (the address the
   dry run shows for indexd), `PAYMENTS_ALEPH_FUND_ADDRESS` and
   `PROVISIONER_ADDRESS` (both the provisioner's address), and
   `PAYMENTS_ALEPH_SPEND_ADDRESS` (the owner's address while the provisioner
   deploys on its behalf).
4. Read a few dry-run cycles, then set `PAYMENTS_TREASURY=live` and
   `PROVISIONER_CREDIT_BUY=live`, and release.
5. Once the provisioner's own credits cover a week, remove
   `ALEPH_OWNER_ADDRESS` so it pays for its VMs itself, set
   `PAYMENTS_ALEPH_SPEND_ADDRESS` to the provisioner's address, and release.

## What each party sees

- TronGrid and PublicNode see which addresses this server asks about.
- The blockchain is public: anyone can see each deposit address's transfers.
- VPS root can read the seed and spend every deposit.
- SimpleSwap sees each swap: the treasury address, the amounts, and the
  indexd and provisioner addresses it pays. Aleph's API and the Ethereum
  node see which addresses' credits and ETH the treasury reads.

## Turning it on

1. A dedicated group for the secrets:

   ```sh
   sudo groupadd --system virtroid-payments
   getent group virtroid-payments | cut -d: -f3
   ```

2. The secrets, using the backend image of the current release:

   ```sh
   sudo virtroid-configure-payments-secrets "$VIRTROID_BACKEND_IMAGE" <gid>
   ```

   Read `/etc/virtroid/secrets/payments-seed` once and copy the 24 words to
   two offline places. They control every deposit.
3. In `.env`: the `TRON_DEPOSIT_XPUB` and `PAYMENTS_PUBLIC_KEY` lines the
   script printed, and `PAYMENTS_SECRET_GID=<gid>`. The release then starts the
   `payments` profile.
4. Optional: a TronGrid API key (free account) raises its rate limits; put it
   in a secret file readable by `PAYMENTS_SECRET_GID` and set
   `TRONGRID_API_KEY_FILE` to that host path in `.env`. Compose mounts it into
   the payments container; without it the service polls keyless.

The app's Wallet screen then shows each user's balance, deposit address and
activity.
