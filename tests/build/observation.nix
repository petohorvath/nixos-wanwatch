# The contract tests of the VM observation helpers, run without
# booting a VM. The Python files live in `tests/vm/`, beside the
# scenarios that load `observation.py`.
{ pkgs, ... }:
let
  observationDir = pkgs.lib.fileset.toSource {
    root = ../vm;
    fileset = pkgs.lib.fileset.unions [
      ../vm/observation.py
      ../vm/test_observation.py
    ];
  };
in
pkgs.runCommand "wanwatch-observation" { } ''
  ${pkgs.python3}/bin/python3 -B -m unittest discover \
    -s ${observationDir} -p test_observation.py -v
  touch $out
''
