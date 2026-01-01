{
  description = "Mobile-friendly web dashboard for tmux sessions";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];

      perSystem = { pkgs, ... }: {
        packages.default = pkgs.buildGoModule {
          pname = "tmux-dashboard";
          version = "0.1.0";
          src = ./.;
          vendorHash = null; # Update after adding dependencies

          meta = with pkgs.lib; {
            description = "Mobile-friendly web dashboard for tmux";
            homepage = "https://github.com/noams/tmux-dashboard";
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
            tmux
          ];

          shellHook = ''
            echo "tmux-dashboard dev shell"
          '';
        };
      };
    };
}
