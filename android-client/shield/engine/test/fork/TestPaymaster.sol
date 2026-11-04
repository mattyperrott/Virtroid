// SPDX-License-Identifier: GPL-3.0-or-later
pragma solidity ^0.8.28;

interface IEntryPoint {
    function depositTo(address account) external payable;
}

struct PackedUserOperation {
    address sender;
    uint256 nonce;
    bytes initCode;
    bytes callData;
    bytes32 accountGasLimits;
    uint256 preVerificationGas;
    bytes32 gasFees;
    bytes paymasterAndData;
    bytes signature;
}

/// A stand-in for a token paymaster on a fork of mainnet, which behaves as the
/// real service's does for the app's purposes: nothing moves during validation
/// (the approval only happens in the user's calls), and the cost is pulled in
/// the token after the calls run. paymasterData is the token and its rate.
contract TestPaymaster {
    IEntryPoint public immutable entryPoint;
    /// Gas the entry point can't see in actualGasCost: this contract's postOp.
    uint256 private constant POST_OP_GAS = 60_000;

    constructor(IEntryPoint _entryPoint) {
        entryPoint = _entryPoint;
    }

    function deposit() external payable {
        entryPoint.depositTo{value: msg.value}(address(this));
    }

    function validatePaymasterUserOp(PackedUserOperation calldata op, bytes32, uint256)
        external
        pure
        returns (bytes memory context, uint256 validationData)
    {
        address token = address(bytes20(op.paymasterAndData[52:72]));
        uint256 rate = uint256(bytes32(op.paymasterAndData[72:104]));
        return (abi.encode(op.sender, token, rate), 0);
    }

    function postOp(uint8, bytes calldata context, uint256 actualGasCost, uint256 actualUserOpFeePerGas) external {
        (address sender, address token, uint256 rate) = abi.decode(context, (address, address, uint256));
        uint256 cost = ((actualGasCost + POST_OP_GAS * actualUserOpFeePerGas) * rate) / 1e18 + 1;
        // USDT's transferFrom returns nothing.
        (bool ok, bytes memory out) = token.call(abi.encodeWithSignature("transferFrom(address,address,uint256)", sender, address(this), cost));
        require(ok && (out.length == 0 || abi.decode(out, (bool))), "token payment failed");
    }
}
