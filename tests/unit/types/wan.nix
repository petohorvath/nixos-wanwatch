/*
  Tests for `lib/types/wan.nix`: per-field validation. `skeleton.nix`
  checks that the `wan` submodule evaluates inputs to what `wan.make`
  builds; `internal/wan.nix` covers the checks option types cannot
  express.
*/
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (helpers) evalSubmodule typeTests;
  inherit (wanwatch) types;

  # `wans.<name>` supplies the name, so inputs leave it out.
  minimal = removeAttrs fixtures.inputs.wan.minimal [ "name" ];
in
{
  wanName = typeTests types.wanName cases.identifiers;
  wanInterface = typeTests types.wanInterface cases.interfaceNames;

  wan = {
    testNameTakesAttributeKey = {
      expr = (evalSubmodule types.wan (minimal // { name = "backup"; })).name;
      expected = "backup";
    };
  }
  // typeTests types.wan {
    invalid = [
      (removeAttrs minimal [ "interface" ])
      (minimal // { interface = "eth 0"; })
      (minimal // { pointToPoint = "yes"; })
      (
        minimal
        // {
          probe = minimal.probe // {
            method = "tcp";
          };
        }
      )
    ];
  };

}
