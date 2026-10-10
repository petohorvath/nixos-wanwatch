# Scanners for the vulnerability audit workflow, kept out of the default
# shell.
{
  govulncheck,
  mkShellNoCC,
  vulnix,
}:
mkShellNoCC {
  packages = [
    govulncheck
    vulnix
  ];
}
