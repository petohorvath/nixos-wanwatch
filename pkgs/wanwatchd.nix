/*
  wanwatchd, the Go daemon that probes WANs, follows rtnetlink events,
  applies routing state, and serves Prometheus metrics (PLAN §8).

    pkgs.callPackage ./wanwatchd.nix {
      version = "0.1.0";
      revision = "abcdef0";
    }

  `version` and `revision` are linked into the binary for
  `wanwatch_build_info`. The flake passes the version from
  `lib/default.nix`; keep the fallback default in sync with it.
  Dependencies are vendored in `daemon/vendor/`, so `vendorHash` is
  null and the build fetches nothing.
*/
{
  lib,
  buildGoModule,
  version ? "0.1.0",
  revision ? "unknown",
}:

assert builtins.isString revision;

buildGoModule {
  pname = "wanwatchd";
  inherit version;

  src = lib.fileset.toSource {
    root = ../daemon;
    fileset = lib.fileset.unions [
      ../daemon/cmd
      ../daemon/internal
      ../daemon/vendor
      ../daemon/go.mod
      ../daemon/go.sum
    ];
  };

  vendorHash = null;

  # Only netns needs cgo, and wanwatch never reaches it.
  env.CGO_ENABLED = "0";

  subPackages = [ "cmd/wanwatchd" ];

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
    "-X main.commit=${revision}"
  ];

  # The tests run in the `daemon`, `coverage`, and `race` flake checks
  # and the VM tier (PLAN §9.4), not in the package build.
  doCheck = false;

  meta = {
    description = "Multi-WAN monitoring and failover daemon for NixOS";
    homepage = "https://github.com/petohorvath/nixos-wanwatch";
    license = lib.licenses.mit;
    mainProgram = "wanwatchd";
    platforms = lib.platforms.linux;
  };
}
