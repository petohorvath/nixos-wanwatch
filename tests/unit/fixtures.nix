/*
  Fixtures shared by the unit suites; `default.nix` passes them to
  every suite as `fixtures`.

    inputs — a minimal and a full valid input per value type. Each
             `full` input sets every field in its serialized form, so
             `toJSONValue (make full)` equals it.
    cases  — valid and invalid values per domain. The predicates, the
             option types, and the `tryMake` fields that share a
             domain all test the same table, so the two layers agree.
*/
let
  probe = {
    minimal = {
      targets.v4 = [ "1.1.1.1" ];
    };
    full = {
      method = "icmp";
      targets = {
        v4 = [ "1.1.1.1" ];
        v6 = [ "2606:4700:4700::1111" ];
      };
      intervalMs = 250;
      timeoutMs = 200;
      windowSize = 20;
      thresholds = {
        lossPctDown = 25;
        lossPctUp = 5;
        rttMsDown = 400;
        rttMsUp = 150;
      };
      hysteresis = {
        consecutiveDown = 2;
        consecutiveUp = 4;
      };
      familyHealthPolicy = "any";
    };
  };

  member = {
    minimal = {
      wan = "primary";
    };
    full = {
      wan = "backup";
      weight = 50;
      priority = 2;
    };
  };
in
{
  inputs = {
    inherit member probe;

    wan = {
      minimal = {
        name = "primary";
        interface = "eth0";
        probe = probe.minimal;
      };
      full = {
        name = "vpn";
        interface = "wg0";
        pointToPoint = true;
        probe = probe.full;
      };
    };

    group = {
      minimal = {
        name = "home-uplink";
        members = [ member.minimal ];
        mark = 1000;
        table = 1000;
      };
      full = {
        name = "guest-uplink";
        members = [
          {
            wan = "primary";
            weight = 100;
            priority = 1;
          }
          member.full
        ];
        strategy = "primary-backup";
        mark = 1001;
        table = 1001;
      };
    };
  };

  cases = {
    # WAN, Group, and Member names. Underscores and dots are stricter
    # than libnet's interface names, so identifiers stay valid unquoted
    # attribute names.
    identifiers = {
      valid = [
        "primary"
        "home-uplink"
        "wan42"
        "A"
      ];
      invalid = [
        ""
        "1primary"
        "two words"
        "home_uplink"
        "home.uplink"
        42
        null
      ];
    };

    positiveInts = {
      valid = [
        1
        2
        32767
      ];
      invalid = [
        0
        (-1)
        1.5
        "1"
        null
      ];
    };

    percentages = {
      valid = [
        0
        50
        100
      ];
      invalid = [
        (-1)
        101
        50.5
        "50"
      ];
    };

    /*
      fwmarks and routing-table IDs share [1000, 32767]. Mark 0 clears
      the mark and would match every unmarked packet; 253–255 are the
      kernel's default, main, and local tables; the floor also keeps
      clear of the small numbers ad-hoc scripts use.
    */
    markTableIds = {
      valid = [
        1000
        16000
        32767
      ];
      invalid = [
        0
        (-1)
        253
        254
        255
        999
        32768
        1000.5
        "1000"
        null
      ];
    };

    # Kernel `dev_valid_name`: shorter than IFNAMSIZ (16 bytes), with no
    # `/`, `:`, or whitespace.
    interfaceNames = {
      valid = [
        "eth0"
        "wwan0"
        "wg0"
        "fifteen-chars-x"
      ];
      invalid = [
        ""
        "sixteen-chars-xx"
        "eth 0"
        "eth/0"
        "eth0:1"
        null
      ];
    };

    booleans = {
      valid = [
        true
        false
      ];
      invalid = [
        "yes"
        1
        null
      ];
    };

    methods = {
      valid = [ "icmp" ];
      invalid = [
        "tcp"
        "http"
      ];
    };

    familyHealthPolicies = {
      valid = [
        "all"
        "any"
      ];
      invalid = [ "majority" ];
    };

    strategies = {
      valid = [ "primary-backup" ];
      invalid = [
        "round-robin"
        "load-balance"
      ];
    };
  };
}
