{
  description = "houston - mission control for AI coding agents in tmux";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    git-hooks-nix.url = "github:cachix/git-hooks.nix";
    git-hooks-nix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [ inputs.git-hooks-nix.flakeModule ];

      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];

      perSystem = { config, pkgs, lib, ... }:
        let
          ui = pkgs.buildNpmPackage {
            pname = "houston-ui";
            version = "0.1.0";
            src = ./ui;
            npmDepsHash = "sha256-VbXiUUSVVP3F+a9H3l4DlVD9wC35v9rBXwY9a1tAKHo=";
            buildPhase = "npm run build";
            installPhase = "cp -r dist $out";
            dontNpmInstall = true;
          };
        in {
          pre-commit.settings.hooks = {
            golangci-lint.enable = true;

            eslint-frontend = {
              enable = true;
              name = "eslint (frontend)";
              entry = "bash -c 'cd ui && npx eslint .'";
              files = "^ui/.*\\.(ts|tsx)$";
              pass_filenames = false;
            };

            typecheck-frontend = {
              enable = true;
              name = "tsc (frontend)";
              entry = "bash -c 'cd ui && npx tsc -b'";
              files = "^ui/.*\\.(ts|tsx)$";
              pass_filenames = false;
            };
          };

          packages.default = pkgs.buildGoModule {
            pname = "houston";
            version = "0.1.0";
            src = pkgs.lib.cleanSource ./.;
            vendorHash = "sha256-0Qxw+MUYVgzgWB8vi3HBYtVXSq/btfh4ZfV/m1chNrA=";

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
            ] ++ config.pre-commit.settings.enabledPackages;

            shellHook = ''
              ${config.pre-commit.shellHook}
              echo "houston dev shell"
            '';
          };
        };
    };
}
