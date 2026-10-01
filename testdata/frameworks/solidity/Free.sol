pragma solidity ^0.8.20;
import "./Base.sol";
function twice(uint value) pure returns (uint) { return plus(value, value); }
function plus(uint a, uint b) pure returns (uint) { return a + b; }
function ambiguous(uint value) pure returns (uint) { return value; }
function ambiguous(address value) pure returns (address) { return value; }
function run(uint value) pure returns (uint) { ambiguous(value); Helpers.wrap(value); return twice(value); }
contract Worker is Base {
    using Helpers for uint;
    uint public count;
    struct Entry { uint value; address owner; }
    enum State { Ready, Done }
    event Changed(uint value);
    error Invalid(uint value);
    modifier valid() { _; }
    constructor() {}
    function target(uint value) public pure returns (uint) { return value; }
    function target(uint a, uint b) public pure returns (uint) { return a + b; }
    function go(uint value) public valid { this.target(value); target(value, value); emit Changed(value); revert Invalid(value); }
    receive() external payable {}
    fallback() external payable {}
}
