{
  description = "houston - mission control for AI coding agents in tmux";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];

      perSystem = { pkgs, lib, ... }:
        let
          # Build the React frontend with npm
          # To get the npmDepsHash run: nix build 2>&1 | grep "got:" | head -1
          ui = pkgs.buildNpmPackage {
            pname = "houston-ui";
            version = "0.1.0";
            src = ./ui;
            npmDepsHash = "sha256-VbXiUUSVVP3F+a9H3l4DlVD9wC35v9rBXwY9a1tAKHo=";
            buildPhase = "npm run build";
            installPhase = "cp -r dist $out";
            # Don't run the default `npm install` install phase
            dontNpmInstall = true;
          };
        in {
          packages.default = pkgs.buildGoModule {
            pname = "houston";
            version = "0.1.0";
            src = pkgs.lib.cleanSource ./.;
            vendorHash = lib.fakeHash;

            # Inject the pre-built React frontend before Go compiles embed.go
            preBuild = ''
              mkdir -p ui/dist
              cp -r ${ui}/* ui/dist/
            '';

            meta = with pkgs.lib; {
              description = "Mission control for AI coding agents in tmux";
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
              just
              tmux
              nodejs
              nodePackages.npm
            ];

            shellHook = ''
              echo "houston dev shell"
            '';
          };
        };
    };
}
