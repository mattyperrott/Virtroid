// SPDX-License-Identifier: GPL-3.0-or-later
//
// The engine's ChainConfig for 0xbow's e2e deployment on a local anvil, at
// `host` (127.0.0.1 for the browser harness, 10.0.2.2 from an emulator).

export function localChain(deployment, host, relayerSigner) {
    const at = (port) => `http://${host}:${port}`;
    return {
        chainId: 11155111,
        name: "Local",
        rpcUrls: [at(8545)],
        explorerUrl: "",
        entrypoint: deployment.entrypointAddress,
        pool: deployment.poolAddress,
        keystore: deployment.keystoreAddress,
        aspRegistry: deployment.aspRegistryAddress,
        relay: deployment.privacyPoolRelayAddress,
        relaySwaps: deployment.relaySwapsAddress,
        fromBlock: deployment.deploymentBlock,
        logsChunk: 50000,
        aspUrl: at(3001),
        relayerUrl: at(3210),
        relayerSigner,
        gasless: null,
        assets: [
            {
                symbol: "ETH",
                token: "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE",
                decimals: 18,
                yield: {
                    ppRouter: deployment.nativeYieldRouterAddress,
                    zap: deployment.nativeYieldZapAddress,
                    ppUSDC: deployment.nativeYieldTokenAddress,
                    ppUSDCDecimals: 18,
                    underlying: deployment.nativeYieldUnderlyingAddress,
                    underlyingDecimals: 18,
                    underlyingSymbol: "WETH",
                    nativeMode: true,
                },
            },
            {
                symbol: "USDC",
                token: deployment.yieldUnderlyingAddress,
                decimals: 6,
                yield: {
                    ppRouter: deployment.yieldRouterAddress,
                    zap: deployment.yieldZapAddress,
                    ppUSDC: deployment.yieldTokenAddress,
                    ppUSDCDecimals: 18,
                    underlying: deployment.yieldUnderlyingAddress,
                    underlyingDecimals: 6,
                    underlyingSymbol: "yUSDC",
                },
            },
        ],
    };
}
