{
  description = "Development shell for app";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let pkgs = import nixpkgs { inherit system; };
      in {
        devShell = pkgs.mkShell {
          name = "api-devshell";

          packages = with pkgs; [
            go
            gopls
            buf
            golangci-lint
          ];

          shellHook = ''
          '';
        };
      });
}
