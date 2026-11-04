// SPDX-License-Identifier: GPL-3.0-or-later
//
// The wallet behind the widget: one set of 12 words, the only backup there is.
// Standard BIP-39 and BIP-44 (m/44'/60'/0'/0/n), so the same words open in any
// Ethereum wallet: address 0 owns the notes and never holds money, and every
// address from 1 up is a deposit address the user is shown once.

import { bytesToHex } from "viem";
import { english, generateMnemonic, mnemonicToAccount, privateKeyToAccount } from "viem/accounts";
import type { PrivateKeyAccount } from "viem/accounts";
import { validateMnemonic } from "@scure/bip39";

import { EngineError } from "./bridge.js";

/** The note owner's index on the words' path. */
export const OWNER_INDEX = 0;
/** The first deposit address's index. */
export const FIRST_DEPOSIT_INDEX = 1;

export function newWords(): string {
    return generateMnemonic(english, 128);
}

/** The words as the app stores them, or an error the user can act on. */
export function readWords(text: string): string {
    const words = text.trim().toLowerCase().split(/\s+/).join(" ");
    if (words.split(" ").length !== 12 || !validateMnemonic(words, english)) {
        throw new EngineError("bad_words", "Those aren't 12 valid backup words");
    }
    return words;
}

export function accountAt(words: string, index: number): PrivateKeyAccount {
    const key = mnemonicToAccount(words, { addressIndex: index }).getHdKey().privateKey;
    if (!key) throw new EngineError("bad_words", "Those words don't make a key");
    return privateKeyToAccount(bytesToHex(key));
}
