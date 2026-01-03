{
  description = "houston - mission control for Claude Code agents in tmux";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];

      perSystem = { pkgs, ... }: {
        packages.default = pkgs.buildGoModule {
          pname = "houston";
          version = "0.1.0";
          src = ./.;
          vendorHash = null; # Update after adding dependencies

          meta = with pkgs.lib; {
            description = "Mission control for Claude Code agents in tmux";
            homepage = "https://github.com/noamsto/houston";
            license = licenses.mit;
          };
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            gotools
            go-tools # staticcheck
            golangci-lint
            air # hot reload
            templ # type-safe HTML templates
            just
            tmux
          ];

          shellHook = ''
            echo "houston dev shell"
          '';
        };
      };
    };
}
