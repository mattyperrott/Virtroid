// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import io.virtroid.shield.engine.WalletRequests

/**
 * A debug build's local test mode (BuildHooks, debug source set): the engine
 * on 0xbow's e2e deployment and the test harness's wallet in place of
 * WalletConnect. Release builds never have one.
 */
class LocalTest(
    /** The engine page under assets/, with the local chain built in. */
    val page: String,
    /** The test wallet's address, or null when the test connects a wallet over WalletConnect. */
    val owner: String?,
    /** The harness's wallet, or null to use WalletConnect (the interop test). */
    val wallet: WalletRequests?,
    /** Plain-HTTP hosts the local stack answers on. */
    val cleartextHosts: Set<String>,
)
