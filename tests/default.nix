/*
  The flake's checks for one system, apart from the formatter check that
  `dev/checks.nix` adds. The daemon, package,
  integration, and VM checks exist only on Linux.

  `inputs`: the flake inputs, including `self`.
  `packages`: the flake's packages for this system.
  `pkgs`: the package set for this system.

  Returns an attrset of check derivations.
*/
{
  inputs,
  packages,
  pkgs,
}:
let
  inherit (pkgs.lib) mapAttrs' nameValuePair optionalAttrs;
  inherit (pkgs.stdenv.hostPlatform) isLinux system;

  runNixUnit = import ./nix-unit.nix { inherit inputs pkgs; };

  vmChecksFor =
    vmPkgs:
    import ./vm {
      inherit (inputs) nftzones;
      inherit (inputs.self) nixosModules;
      pkgs = vmPkgs;
    };

  prefixNames = prefix: mapAttrs' (name: value: nameValuePair "${prefix}${name}" value);
in
{
  unit = runNixUnit "unit";
  # statix and deadnix scan the whole repository.
  lint =
    pkgs.runCommand "wanwatch-lint"
      {
        nativeBuildInputs = [
          pkgs.deadnix
          pkgs.statix
        ];
      }
      ''
        cd ${inputs.self}
        statix check .
        deadnix --fail .
        touch $out
      '';
  # Tests the VM observation helpers without booting a VM.
  observation = pkgs.runCommand "wanwatch-observation-tests" { } ''
    ${pkgs.python3}/bin/python3 -B -m unittest discover \
      -s ${./vm} -p test_observation.py -v
    touch $out
  '';
}
// optionalAttrs isLinux (
  import ./daemon.nix { inherit pkgs; }
  // {
    # Catches packaging regressions, such as a file missing from the
    # fileset, in `nix flake check`.
    package = packages.wanwatchd;
    integration = runNixUnit "integration.${system}";
  }
  # `vm-*` uses stable nixpkgs, as releases do; `vm-unstable-*` previews
  # the kernel and systemd that stable will get next.
  // prefixNames "vm-" (vmChecksFor pkgs)
  // prefixNames "vm-unstable-" (vmChecksFor inputs.nixpkgs-unstable.legacyPackages.${system})
)
