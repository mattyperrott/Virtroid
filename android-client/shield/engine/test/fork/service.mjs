// SPDX-License-Identifier: GPL-3.0-or-later
//
// A stand-in for the gas service on a fork of mainnet: the bundler and token
// paymaster methods gasless.ts calls, against TestPaymaster. It estimates
// generously, then sends the UserOperation with handleOps from a funded key,
// as a type-4 transaction when it carries a 7702 authorization. What it can't
// stand in for is the real service's API, which only a funded address reaches.

import { pathToFileURL } from "node:url";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const viemDir = join(here, "../../.sdk/pp2/packages/evm-sdk/node_modules/viem/_esm");
const { encodeFunctionData, encodePacked, toHex, keccak256 } = await import(pathToFileURL(join(viemDir, "index.js")));
const { entryPoint08Abi, entryPoint08Address, toPackedUserOperation } = await import(pathToFileURL(join(viemDir, "account-abstraction/index.js")));

export function gasService({ publicClient, bundler, paymaster, rates, log }) {
    const receipts = new Map();
    const parse = (op) => ({
        sender: op.sender,
        nonce: BigInt(op.nonce),
        callData: op.callData,
        callGasLimit: BigInt(op.callGasLimit),
        verificationGasLimit: BigInt(op.verificationGasLimit),
        preVerificationGas: BigInt(op.preVerificationGas),
        maxFeePerGas: BigInt(op.maxFeePerGas),
        maxPriorityFeePerGas: BigInt(op.maxPriorityFeePerGas),
        paymaster: op.paymaster,
        paymasterVerificationGasLimit: BigInt(op.paymasterVerificationGasLimit ?? 0),
        paymasterPostOpGasLimit: BigInt(op.paymasterPostOpGasLimit ?? 0),
        paymasterData: op.paymasterData,
        signature: op.signature,
    });
    const paymasterFields = (token) => ({
        paymaster,
        paymasterData: encodePacked(["address", "uint256"], [token, rates[token.toLowerCase()]]),
        paymasterVerificationGasLimit: toHex(60_000n),
        paymasterPostOpGasLimit: toHex(70_000n),
    });
    return {
        async pm_supportedERC20Tokens() {
            return {
                paymasterMetadata: { address: paymaster },
                tokens: Object.entries(rates).map(([address, rate]) => ({ address, exchangeRate: toHex(rate) })),
            };
        },
        async voltaire_feesPerGas() {
            const block = await publicClient.getBlock();
            const tip = 20_000_000n;
            return { maxFeePerGas: toHex((block.baseFeePerGas * 3n) / 2n + tip), maxPriorityFeePerGas: toHex(tip) };
        },
        async pm_getPaymasterStubData([, , , context]) {
            return paymasterFields(context.token);
        },
        async pm_getPaymasterData([, , , context]) {
            return paymasterFields(context.token);
        },
        async eth_estimateUserOperationGas([op]) {
            // What the real deposit measured, with room, as a bundler's are.
            log?.(`estimate: ${op.callData.length / 2} bytes of calldata, auth ${op.eip7702Auth ? "yes" : "no"}`);
            const calldataGas = BigInt(Math.ceil(op.callData.length / 2)) * 16n;
            return { callGasLimit: toHex(1_350_000n), verificationGasLimit: toHex(110_000n), preVerificationGas: toHex(60_000n + calldataGas) };
        },
        async eth_sendUserOperation([op]) {
            const parsed = parse(op);
            const auth = op.eip7702Auth;
            const authorizationList = auth
                ? [{ address: auth.address, chainId: Number(auth.chainId), nonce: Number(auth.nonce), r: auth.r, s: auth.s, yParity: Number(auth.yParity) }]
                : undefined;
            const data = encodeFunctionData({
                abi: entryPoint08Abi,
                functionName: "handleOps",
                args: [[toPackedUserOperation(parsed)], bundler.account.address],
            });
            const hash = await bundler.sendTransaction({ to: entryPoint08Address, data, gas: 3_500_000n, authorizationList });
            const receipt = await publicClient.waitForTransactionReceipt({ hash });
            const opHash = keccak256(hash);
            const event = receipt.logs.find((l) => l.address.toLowerCase() === entryPoint08Address.toLowerCase() && l.topics[0] === USER_OP_EVENT);
            // The event's data: nonce, success, actualGasCost, actualGasUsed.
            const success = event ? BigInt(`0x${event.data.slice(66, 130)}`) === 1n : false;
            log?.(`handleOps ${receipt.status}: ${receipt.gasUsed} gas, op success ${success}`);
            for (const l of receipt.logs) {
                if (l.topics[0] === REVERT_EVENT) log?.(`user operation reverted: ${l.data.slice(0, 700)}`);
                if (l.topics[0] === POSTOP_REVERT_EVENT) log?.(`postOp reverted: ${l.data.slice(0, 700)}`);
            }
            receipts.set(opHash, { success, actualGasCost: event ? toHex(BigInt(`0x${event.data.slice(130, 194)}`)) : "0x0", receipt: { transactionHash: hash } });
            return opHash;
        },
        async eth_getUserOperationReceipt([hash]) {
            return receipts.get(hash) ?? null;
        },
    };
}

const text = (t) => keccak256(new TextEncoder().encode(t));
const USER_OP_EVENT = text("UserOperationEvent(bytes32,address,address,uint256,bool,uint256,uint256)");
const REVERT_EVENT = text("UserOperationRevertReason(bytes32,address,uint256,bytes)");
const POSTOP_REVERT_EVENT = text("PostOpRevertReason(bytes32,address,uint256,bytes)");
