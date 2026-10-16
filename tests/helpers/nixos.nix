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

    Returns the system's `config`.
  */
  evalNixosWith =
    {
      wanwatchModules ? nixosModules,
      telegraf ? false,
    }:
    modules:
    (nixpkgs.lib.nixosSystem {
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
    }).config;
in
{
  inherit evalNixosWith;

  # `evalNixosWith` with the source modules and no Telegraf module.
  evalNixos = evalNixosWith { };

  /*
    Parse the daemon config that a system renders. Rendering forces
    `wan.make` and `group.make` on every declaration.

    `config`: a system's `config`, as `evalNixos` returns it.

    Returns the parsed `/etc/wanwatch/config.json`.
  */
  renderConfig = config: builtins.fromJSON config.environment.etc."wanwatch/config.json".text;
}
