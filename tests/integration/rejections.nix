# Declarations that NixOS evaluation rejects. They prove that the
# module still routes declarations through the library validators,
# which the unit suites call directly.
{ fixtures, helpers, ... }:
let
  inherit (helpers) evalNixos renderConfig;

  evalOneWan = faults: evalNixos [ (fixtures.nixos.oneWan faults) ];

  # ADR-0014: the module owns the daemon's identity and paths, so
  # setting a former option fails evaluation.
  rejectsRemovedOption = optionPattern: definition: {
    expr = renderConfig (evalNixos [
      fixtures.nixos.primaryAndBackup
      { services.wanwatch = definition; }
    ]);
    expectedError = {
      type = "ThrownError";
      msg = "${optionPattern}' does not exist";
    };
  };
in
{
  testProbeNoTargets = {
    expr = renderConfig (evalOneWan {
      targets = {
        v4 = [ ];
        v6 = [ ];
      };
    });
    expectedError = {
      type = "ThrownError";
      msg = "\\[probeNoTargets]";
    };
  };

  testProbeFamilyMismatch = {
    expr = renderConfig (evalOneWan {
      targets.v4 = [ "2606:4700:4700::1111" ];
    });
    expectedError = {
      type = "ThrownError";
      msg = "\\[probeTargetFamilyMismatch]";
    };
  };

  testGroupNoMembers = {
    expr = renderConfig (evalOneWan {
      members = { };
    });
    expectedError = {
      type = "ThrownError";
      msg = "\\[groupNoMembers]";
    };
  };

  removedOptions = {
    testUser = rejectsRemovedOption "wanwatch\\.user" { user = "root"; };
    testGroup = rejectsRemovedOption "wanwatch\\.group" { group = "network"; };
    testStatePath = rejectsRemovedOption "global\\.statePath" {
      global.statePath = "/var/lib/wanwatch/state.json";
    };
    testHooksDir = rejectsRemovedOption "global\\.hooksDir" {
      global.hooksDir = "/var/lib/wanwatch/hooks";
    };
    testMetricsSocket = rejectsRemovedOption "global\\.metricsSocket" {
      global.metricsSocket = "/var/lib/wanwatch/metrics.sock";
    };
  };

  # A typo in a Member's WAN fails here rather than as a daemon
  # restart loop.
  testMemberUndeclaredWan = {
    expr = renderConfig (evalNixos [
      fixtures.nixos.primaryAndBackup
      {
        services.wanwatch.groups.home-uplink.members.bakcup = { };
      }
    ]);
    expectedError = {
      type = "ThrownError";
      msg = "group 'home-uplink' references wan 'bakcup'";
    };
  };
}
