// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

/**
 * A debug build's local test mode (BuildHooks, debug source set): the engine
 * on 0xbow's e2e deployment. Release builds never have one.
 */
class LocalTest(
    /** The engine page under assets/, with the local chain built in. */
    val page: String,
    /** Plain-HTTP hosts the local stack answers on. */
    val cleartextHosts: Set<String>,
)
