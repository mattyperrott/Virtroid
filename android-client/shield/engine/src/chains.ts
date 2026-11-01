// SPDX-License-Identifier: GPL-3.0-or-later
//
// The Privacy Pools v2 deployments the app serves. 0xbow deployed the same
// contract addresses on every chain; verify-chains.mjs reads each value back
// from the chain (code at every address, the pool's own keystore, ASP
// registry and access router, each router's share, entrypoint and pool) and
// is what to run before changing anything here.
//
// On these chains the pool holds stablecoins and ETH as 0xbow's Aave-backed
// shares (ppUSDC, ppUSDT, ppETH): a shield wraps the user's USDC, USDT or ETH
// through the asset's router, a withdrawal unwraps back to it. Plain USDC and
// USDT pools there take no new deposits. Base has no ppUSDT, so its USDT is
// a plain pool.

import type { Address, Hex, YieldDeployment } from "@privacy-pools-v2/evm-sdk";

export interface Asset {
    /** What the user holds and sees: ETH, USDC, USDT. */
    symbol: string;
    /** The ERC-20 the user deposits, or NATIVE for ETH. */
    token: Address;
    decimals: number;
    /** The router that wraps it into the pool's share, or null for a plain pool. */
    yield: YieldDeployment | null;
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
    assets: Asset[];
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

function plain(symbol: string, token: Address, decimals: number): Asset {
    return { symbol, token, decimals, yield: null };
}

const a = (address: string) => address as Address;
const block = (n: number) => `0x${n.toString(16)}` as Hex;

export const CHAINS: ChainConfig[] = [
    {
        chainId: 42161,
        name: "Arbitrum",
        rpcUrls: ["https://arb1.arbitrum.io/rpc", "https://arbitrum-one-rpc.publicnode.com"],
        explorerUrl: "https://arbiscan.io",
        ...core,
        fromBlock: block(514_746_410),
        logsChunk: 50_000n,
        assets: [
            share("ETH", a("0x82aF49447D8a07e3bd95BD0d56f35241523fBab1"), 18,
                a("0xc0FBB5D5936a6f089C1B30Ff0Fd0BEAd27b9F766"), a("0x6C17bE9c864254CA4b2d36223f31AdE94B4d64c2"),
                a("0x319aBEE703b5f6c3dB9c2f126475ef49B20Fdd49"), true),
            share("USDC", a("0xaf88d065e77c8cC2239327C5EDb3A432268e5831"), 6,
                a("0xaaE55aFcF94c1604C5713CD19510c5140e8Afa0e"), a("0x34F9F5c4788a60eae7A8901d83E0bbe56Ab43251"),
                a("0x4a5B11c3898367664D94B43bE1DcEA163BB1D094")),
            share("USDT", a("0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9"), 6,
                a("0x19918499f1d06162A18B208cA4c97032A828dF0a"), a("0x12a78A1EA6F12d2a2cF37070cF93c872BFC2Da7E"),
                a("0xFe4DcdDD59027384462207c2b9101F9CDcd58a3C")),
        ],
    },
    {
        chainId: 8453,
        name: "Base",
        rpcUrls: ["https://mainnet.base.org", "https://base-rpc.publicnode.com"],
        explorerUrl: "https://basescan.org",
        ...core,
        fromBlock: block(52_595_825),
        logsChunk: 2_000n,
        assets: [
            share("ETH", a("0x4200000000000000000000000000000000000006"), 18,
                a("0xc0FBB5D5936a6f089C1B30Ff0Fd0BEAd27b9F766"), a("0x6C17bE9c864254CA4b2d36223f31AdE94B4d64c2"),
                a("0x319aBEE703b5f6c3dB9c2f126475ef49B20Fdd49"), true),
            share("USDC", a("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"), 6,
                a("0xaaE55aFcF94c1604C5713CD19510c5140e8Afa0e"), a("0x34F9F5c4788a60eae7A8901d83E0bbe56Ab43251"),
                a("0x4a5B11c3898367664D94B43bE1DcEA163BB1D094")),
            plain("USDT", a("0xfde4C96c8593536E31F229EA8f37b2ADa2699bb2"), 6),
        ],
    },
    {
        chainId: 10,
        name: "Optimism",
        rpcUrls: ["https://mainnet.optimism.io", "https://optimism-rpc.publicnode.com"],
        explorerUrl: "https://optimistic.etherscan.io",
        ...core,
        fromBlock: block(158_189_808),
        logsChunk: 3_000n,
        assets: [
            share("ETH", a("0x4200000000000000000000000000000000000006"), 18,
                a("0xc0FBB5D5936a6f089C1B30Ff0Fd0BEAd27b9F766"), a("0x6C17bE9c864254CA4b2d36223f31AdE94B4d64c2"),
                a("0x319aBEE703b5f6c3dB9c2f126475ef49B20Fdd49"), true),
            share("USDC", a("0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85"), 6,
                a("0xaaE55aFcF94c1604C5713CD19510c5140e8Afa0e"), a("0x34F9F5c4788a60eae7A8901d83E0bbe56Ab43251"),
                a("0x4a5B11c3898367664D94B43bE1DcEA163BB1D094")),
            share("USDT", a("0x94b008aA00579c1307B0EF2c499aD98a8ce58e58"), 6,
                a("0x19918499f1d06162A18B208cA4c97032A828dF0a"), a("0x12a78A1EA6F12d2a2cF37070cF93c872BFC2Da7E"),
                a("0xFe4DcdDD59027384462207c2b9101F9CDcd58a3C")),
        ],
    },
    {
        chainId: 1,
        name: "Ethereum",
        rpcUrls: ["https://ethereum-rpc.publicnode.com"],
        explorerUrl: "https://etherscan.io",
        ...core,
        fromBlock: block(25_884_228),
        logsChunk: 50_000n,
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
