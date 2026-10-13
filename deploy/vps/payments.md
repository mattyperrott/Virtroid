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

Sweeping deposits into a treasury and converting them into Siacoin and Aleph
credits are not built yet; deposits stay on their addresses until then.

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
  into one ledger entry per runtime and hour. Runtimes keep running for
  `RUNTIME_IDLE_TIMEOUT` after the app disconnects unless the user stops them,
  and that time counts towards the trial hour and is charged too.
- Storage: `STORAGE_PRICE_PER_GIB_MONTH_MICRO` (default 50000, i.e. 0.05 USDT)
  per GiB of stored runtime data, local or on Sia, per 30 days, charged once a
  day.
- Starting a runtime needs ten minutes of runtime credit. When the balance
  reaches zero, the account's runtimes are stopped. Their stored data is kept
  and storage charges continue, so the balance can go below zero.

The defaults are placeholders: set both prices in `.env` before taking
deposits. The app's Wallet screen shows them.

## What each party sees

- TronGrid and PublicNode see which addresses this server asks about.
- The blockchain is public: anyone can see each deposit address's transfers.
- VPS root can read the seed and spend every deposit.

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
   in a secret file and point `TRONGRID_API_KEY_FILE` at it.

The app's Wallet screen then shows each user's balance, deposit address and
activity.
