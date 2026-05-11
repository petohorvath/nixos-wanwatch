/*
  types/wan — NixOS option types for the WAN value type.

  Pass 1 boundary: empty stub. Pass 5 will export `wan`, `wanName`,
  `wanInterface`, `wanGateways`, `wanGatewayV4`, `wanGatewayV6`,
  and any related primitives — flattened into `wanwatch.types.<name>`
  by `lib/types/default.nix`.
*/
{
  lib,
  libnet,
  primitives,
  internal,
}:
{ }
