# Nix Git Hooks Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add pre-commit hooks (golangci-lint, eslint, tsc) via git-hooks.nix flake-parts module.

**Architecture:** Add `git-hooks-nix` flake input, configure three hooks in `perSystem`, wire shellHook into devShell. Hooks auto-install on `nix develop`.

**Tech Stack:** git-hooks.nix (cachix), flake-parts, golangci-lint, eslint, tsc

---

### Task 1: Add git-hooks-nix flake input

**Files:**
- Modify: `flake.nix:1-7` (inputs block)

**Step 1: Add the input**

In `flake.nix`, add `git-hooks-nix` to the inputs block:

```nix
inputs = {
  nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  flake-parts.url = "github:hercules-ci/flake-parts";
  git-hooks-nix.url = "github:cachix/git-hooks.nix";
  git-hooks-nix.inputs.nixpkgs.follows = "nixpkgs";
};
```

**Step 2: Update flake.lock**

Run: `nix flake update git-hooks-nix`
Expected: flake.lock updated with git-hooks-nix entry

**Step 3: Commit**

```
git add flake.nix flake.lock
git commit -m "chore: add git-hooks-nix flake input"
```

---

### Task 2: Import flake module and configure hooks

**Files:**
- Modify: `flake.nix:9-66` (outputs block)

**Step 1: Import the flake module**

Add imports list after the `systems` line:

```nix
outputs = inputs@{ flake-parts, ... }:
  flake-parts.lib.mkFlake { inherit inputs; } {
    imports = [ inputs.git-hooks-nix.flakeModule ];

    systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
```

**Step 2: Add hook configuration**

Add `pre-commit.settings.hooks` inside `perSystem`, before the `let` block:

```nix
perSystem = { config, pkgs, lib, ... }: {
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
```

Note: `perSystem` signature changes — add `config` parameter.

**Step 3: Wire shellHook and packages into devShell**

Update the devShell to include the git-hooks shellHook and packages:

```nix
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
```

**Step 4: Verify the flake evaluates**

Run: `nix flake check --no-build`
Expected: No evaluation errors

**Step 5: Commit**

```
git add flake.nix
git commit -m "feat: add pre-commit hooks — golangci-lint, eslint, tsc"
```

---

### Task 3: Test hooks work

**Step 1: Install hooks by re-entering devShell**

Run: `direnv reload` (or `nix develop`)
Expected: pre-commit hooks installed message

**Step 2: Run all hooks manually**

Run: `pre-commit run -a`
Expected: All three hooks run. golangci-lint, eslint, and tsc should all pass on current codebase.

**Step 3: Test with a staged Go file**

Run: `git stash && git stash pop && pre-commit run golangci-lint`
Expected: golangci-lint runs on staged Go files

**Step 4: If any hook fails, fix the issue and re-run**

The codebase should be clean, but if there are lint issues, fix them before finalizing.

---

### Task 4: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

**Step 1: Add hooks section to CLAUDE.md**

Add after the "Development Workflow" section in the Development Setup area:

```markdown
### Pre-commit Hooks

Managed by `git-hooks.nix`. Auto-installed on `nix develop` / `direnv reload`.

- **golangci-lint** — runs on staged `.go` files
- **eslint** — runs `npm run lint` in `ui/` when `.ts`/`.tsx` files change
- **tsc** — runs `tsc -b` in `ui/` when `.ts`/`.tsx` files change

Run all hooks manually: `pre-commit run -a`
Skip hooks: `git commit --no-verify`
```

**Step 2: Commit**

```
git add CLAUDE.md
git commit -m "docs: document pre-commit hooks in CLAUDE.md"
```
