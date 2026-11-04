// SPDX-License-Identifier: GPL-3.0-or-later
//
// The Privacy Pools v2 deployment the app serves: Ethereum, with ETH, USDC and
// USDT. verify-chains.mjs reads each value back from the chain (code at every
// address, the pool's own keystore, ASP registry and access router, each
// router's share, entrypoint and pool) and is what to run before changing
// anything here.
//
// The pool holds all three as 0xbow's Aave-backed shares (ppETH, ppUSDC,
// ppUSDT): a shield wraps the asset through its router, a withdrawal unwraps
// back to it. Plain USDC and ETH pools take no new deposits.

import type { Address, Hex, YieldDeployment } from "@privacy-pools-v2/evm-sdk";

export interface Asset {
    /** What the user holds and sees: ETH, USDC, USDT. */
    symbol: string;
    /** The ERC-20 the user deposits, or NATIVE for ETH. */
    token: Address;
    decimals: number;
    /** The router that wraps it into the pool's share. */
    yield: YieldDeployment;
}

export interface ChainConfig {
    chainId: number;
    name: string;
    /** Tried in order; the user can put their own first. */
    rpcUrls: string[];
    explorerUrl: string;
    entrypoint: Address;
    pool: Address;
    keystore: Address;
    aspRegistry: Address;
    /** PrivacyPoolRelay: the processor a relayed withdrawal binds. */
    relay: Address;
    relaySwaps: Address;
    /** The keystore's deploy block, where every scan starts. */
    fromBlock: Hex;
    /** The widest eth_getLogs window the public RPCs answer. */
    logsChunk: bigint;
    aspUrl: string;
    relayerUrl: string;
    /** The relayer's signer: msg.sender, and so the bound processor, on the transfer route. */
    relayerSigner: Address;
    /** Who takes gas in USDC or USDT for an address that holds no ETH; none on the local test chain. */
    gasless: GaslessConfig | null;
    assets: Asset[];
}

/** An ERC-4337 bundler and token paymaster, reached with an EIP-7702 delegation (gasless.ts). */
export interface GaslessConfig {
    /** Answers both the bundler's and the paymaster's methods. */
    url: string;
    /** Refused if the service names another: it's the spender the app approves. */
    paymaster: Address;
    /** eth-infinitism's Simple7702Account for EntryPoint 0.8, the code a deposit address delegates to. */
    delegate: Address;
}

export const NATIVE = "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE" as Address;

const ENTRYPOINT = "0xca1e072236D231130059eeA6Bfceb7F4e9a1F0C3" as Address;
const POOL = "0x0Eb42804BF897662aF851370eaeAdcDBEC1b97ed" as Address;
const KEYSTORE = "0x6E463391c79b7e1A88eF26E0B023f9A6C9938a71" as Address;
const ASP_REGISTRY = "0x259C0BEa1d783c202509BD3b2688d23b8AEf58B0" as Address;
const RELAY = "0x01a8Fa4dbb60187aD8d3c3d08E7Fb8C14198A461" as Address;
const RELAY_SWAPS = "0xd0395c08DD8eA7671a7A65bDca8E473e8E8260a8" as Address;
const ASP_URL = "https://api-v2.0xbow.io";
const RELAYER_URL = "https://relayer-v2-prod-149184580131.us-east1.run.app";
const RELAYER_SIGNER = "0x9e2f1B234953F46c6091636290E9035618EA3C07" as Address;

const core = {
    entrypoint: ENTRYPOINT,
    pool: POOL,
    keystore: KEYSTORE,
    aspRegistry: ASP_REGISTRY,
    relay: RELAY,
    relaySwaps: RELAY_SWAPS,
    aspUrl: ASP_URL,
    relayerUrl: RELAYER_URL,
    relayerSigner: RELAYER_SIGNER,
};

function share(
    symbol: string,
    token: Address,
    decimals: number,
    router: Address,
    zap: Address,
    shareToken: Address,
    nativeMode = false,
): Asset {
    return {
        symbol,
        token: nativeMode ? NATIVE : token,
        decimals,
        yield: {
            ppRouter: router,
            zap,
            ppUSDC: shareToken,
            ppUSDCDecimals: 18,
            underlying: token,
            underlyingDecimals: decimals,
            underlyingSymbol: nativeMode ? "WETH" : symbol,
            ...(nativeMode ? { nativeMode: true } : {}),
            apyLabel: "Aave v3",
        },
    };
}

const a = (address: string) => address as Address;
const block = (n: number) => `0x${n.toString(16)}` as Hex;

export const CHAINS: ChainConfig[] = [
    {
        chainId: 1,
        name: "Ethereum",
        // The public RPCs that answered historical logs on 2026-11-04 from a 5,000-block
        // window up (publicnode, llamarpc, cloudflare and others refuse or cap far below it).
        rpcUrls: [
            "https://rpc.mevblocker.io",
            "https://gateway.tenderly.co/public/mainnet",
            "https://ethereum.public.blockpi.network/v1/rpc/public",
            "https://0xrpc.io/eth",
        ],
        explorerUrl: "https://etherscan.io",
        ...core,
        fromBlock: block(25_884_228),
        logsChunk: 5_000n,
        gasless: {
            url: "https://api.candide.dev/public/v3/1",
            paymaster: a("0xa8151918eac3818deb713d3dbbb7930329fe86ed"),
            delegate: a("0xe6Cae83BdE06E4c305530e199D7217f42808555B"),
        },
        assets: [
            share("ETH", a("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), 18,
                a("0x77613D670BB9fD48A9898D0eac8ebf21B566F960"), a("0x33c1eEf370bBb447414156CFc4fDABD434776EAf"),
                a("0x66051920D25af4E3596A8a46309F4D045F9D83B7"), true),
            share("USDC", a("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), 6,
                a("0xe76E793d776c1e207aD7a7fa7C57d27420d85110"), a("0x9Ea986BcDF6784D85B518b15bEEE1E65b585817B"),
                a("0xC246aFb23482fF9596E9cee5f1f678eFe0EB1ad6")),
            share("USDT", a("0xdAC17F958D2ee523a2206206994597C13D831ec7"), 6,
                a("0x13a0b86bac3e0C29bd64fED7BCD2EE84f371f41C"), a("0xDC3D922df81FC7B7f58229431ED91acB0482f3A5"),
                a("0xA4cF419558Ef36CC6FfB666195684dC963910426")),
        ],
    },
];

export function chainById(chainId: number): ChainConfig {
    const chain = CHAINS.find((c) => c.chainId === chainId);
    if (!chain) throw new Error(`Chain ${chainId} isn't served`);
    return chain;
}
