// SPDX-License-Identifier: GPL-3.0-or-later
// Node's crypto, as much of it as the SDK's (unused here) file storage imports.
export const randomUUID = () => globalThis.crypto.randomUUID();
export default { randomUUID };
