// SPDX-License-Identifier: GPL-3.0-or-later
// Node's assert, as much of it as circomlibjs's Poseidon uses.
export default function assert(condition, message) {
    if (!condition) throw new Error(message || "Assertion failed");
}
