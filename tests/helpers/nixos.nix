/*
  Helpers that evaluate the wanwatch NixOS modules in a NixOS system,
  without a VM or kernel; `test-context.nix` passes them to every suite
  in `helpers`.

  `nixosModules`: the wanwatch modules imported from source, `default`
  and `telegraf`.
  `nixpkgs`: the nixpkgs flake providing `nixosSystem`.
  `system`: the host platform of the evaluated systems.
*/
{
  nixosModules,
  nixpkgs,
  system,
}:
let
  /*
    Evaluate a container NixOS system with the wanwatch module.

    `wanwatchModules`: the wanwatch modules, `default` and `telegraf`;
    the source modules unless a suite compares other ones.
    `telegraf`: whether to import the Telegraf module too.
    `modules`: further modules, such as a `services.wanwatch`
    declaration.

    Returns the evaluated system, with its `config` and `options`.
  */
  evalSystemWith =
    {
      wanwatchModules ? nixosModules,
      telegraf ? false,
    }:
    modules:
    nixpkgs.lib.nixosSystem {
      modules = [
        wanwatchModules.default
        {
          nixpkgs.hostPlatform = system;
          boot.isContainer = true;
          system.stateVersion = "24.11";
        }
      ]
      ++ nixpkgs.lib.optional telegraf wanwatchModules.telegraf
      ++ modules;
    };

  # `evalSystemWith`, returning the system's `config`.
  evalNixosWith = evalArgs: modules: (evalSystemWith evalArgs modules).config;
in
{
  inherit evalNixosWith;

  # `evalNixosWith` with the source modules and no Telegraf module.
  evalNixos = evalNixosWith { };

  # The option declarations of a system with the source modules, for
  # suites that test option documentation and types.
  evalNixosOptions = modules: (evalSystemWith { } modules).options;

  /*
    List the messages of a system's failed wanwatch assertions, which
    NixOS would report when building it.

    `config`: a system's `config`, as `evalNixos` returns it.

    Returns the messages of the failed assertions whose message starts
    with `services.wanwatch`, so other modules' assertions don't leak
    into a wanwatch test.
  */
  failedWanwatchAssertions =
    config:
    nixpkgs.lib.pipe config.assertions [
      (builtins.filter (assertion: !assertion.assertion))
      (map (assertion: assertion.message))
      (builtins.filter (nixpkgs.lib.hasPrefix "services.wanwatch"))
    ];

  /*
    Parse the daemon config that a system renders. Rendering forces
    `wan.make` and `group.make` on every declaration.

    `config`: a system's `config`, as `evalNixos` returns it.

    Returns the parsed `/etc/wanwatch/config.json`.
  */
  renderConfig = config: builtins.fromJSON config.environment.etc."wanwatch/config.json".text;

  /*
    The flags that link a wanwatchd package's revision into the binary
    for `wanwatch_build_info`.

    `package`: a wanwatchd package, such as a system's
    `services.wanwatch.package`.

    Returns the package's `-X main.revision=` ldflags.

      revisionFlags config.services.wanwatch.package
      # => [ "-X main.revision=unknown" ]
  */
  revisionFlags =
    package: builtins.filter (nixpkgs.lib.hasPrefix "-X main.revision=") package.ldflags;
}
