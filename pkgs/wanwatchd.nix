/*
  wanwatchd — the wanwatch Go daemon. Drives one ICMP prober per
  (WAN, family), listens on rtnetlink, mutates kernel routing, and
  exposes Prometheus metrics over a Unix socket. PLAN §8.

  Vendored deps live under `daemon/vendor/` so the build is hermetic
  (`vendorHash = null`, no proxy/sumdb fetches). `CGO_ENABLED = 0`
  because the only cgo-using transitive dep (netns) is unreachable
  from wanwatch.

  Version + commit are link-injected so `wanwatch_build_info` matches
  the package store path. Callers override via:

    pkgs.callPackage ./wanwatchd.nix {
      version = "0.1.0";
      commit  = "abcdef0";
    }
*/
{
  lib,
  buildGoModule,
  version ? "dev",
  commit ? "unknown",
}:

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

  env.CGO_ENABLED = "0";

  subPackages = [ "cmd/wanwatchd" ];

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
    "-X main.commit=${commit}"
  ];

  # Daemon talks to the kernel via netlink — no useful test surface
  # here that buildGoModule can run unprivileged. Real coverage is
  # the `daemon` flake check (which runs `go test ./...` in the
  # sandbox) plus the VM tier (PLAN §9.4).
  doCheck = false;

  meta = {
    description = "Multi-WAN monitoring and failover daemon for NixOS";
    homepage = "https://github.com/petohorvath/nixos-wanwatch";
    license = lib.licenses.mit;
    mainProgram = "wanwatchd";
    platforms = lib.platforms.linux;
  };
}
