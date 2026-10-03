# AGENTS.md

## Mission

This repository contains a **Go based kubernetes CSI driver**.
This document defines **strict operational rules for automated coding agents** modifying the repository.

Agents must prioritize:

1. **Repository stability**
2. **Deterministic builds**
3. **Consistent formatting**
4. **Minimal and correct changes**

The repository must **always remain buildable, testable, and containerizable** after any modification.

If a task conflicts with this document, **this document takes precedence**.

---

# Repository Architecture

Technology stack:

* Golang
* Docker for container images
* Makefile as the canonical command interface

The project prioritizes **simplicity, fast development loops, and reproducibility**.

---

# Golden Rules

Agents **must never** leave the repository in a broken state.

After any modification the following must succeed:

```
make fmt
make test
make docker-build
```

If any of these fail, the change is incomplete.

---

# Command Interface

## Makefile is Authoritative

All workflows must be accessible through the **Makefile**.

Agents must **prefer Make targets over raw commands**.

If an agent introduces a new developer workflow, the **Makefile must be updated**.

---

# Formatting Rules

Use standard go fmt.

Agents must run formatting after editing Go files.

Command:

```
make fmt
```

Unformatted code must never be committed.

---

## General Code Principles

Agents should:

* prefer immutability
* avoid unnecessary abstraction
* keep methods small and readable
* follow existing project patterns

Avoid introducing new architectural styles without strong justification.

# Container Build Rules

Container images must be built using **Docker directly**.

The repository must contain a **Dockerfile**.

Example build command:

```
docker build -t service-name .
```

Agents must ensure container builds remain functional.

---

# Dependency Policy

When adding dependencies:

Priority order:

1. small focused libraries

Avoid:

* large frameworks
* overlapping libraries
* unnecessary utility packages

Agents must keep dependencies minimal.

---

# Safe File Modification Rules

Agents must treat the following files as **high-risk**:

* `Dockerfile`
* `Makefile`

Modify these only when necessary.

If modified, the full validation workflow must succeed.

---

# GitHub Workflow

Agents must use the **`gh` CLI** for all GitHub interactions (PRs, issues, reviews, CI status).

Manual `git push` of a branch without a PR, or handcrafted GitHub web actions, are **not allowed**.

## Branch Rules

At the **start** of a new task, agents must create a dedicated branch:

```
git checkout main
git pull
git checkout -b <type>/<short-description>
```

Rules:

* branch from **main**, unless the user explicitly instructs otherwise
* branch names use `<type>/<short-description>` (`fix/`, `feat/`, `chore/`, `docs/`, `refactor/`)
* never work directly on `main`
* never force-push or amend shared history

## Pull Request Rules

When the task is complete and all validation steps pass, agents must:

1. push the branch
2. open a pull request with `gh`

```
git push -u origin <branch>
gh pr create --base main --title "<type>: <summary>" --body "<description>"
```

PR description requirements:

* **minimal yet sufficient**: what changed and why
* reference related issues with `Closes #<n>` when applicable
* list the validation commands that were run (`make fmt`, `make test`, `make docker-build`)
* no boilerplate, no template padding, no redundant restating of the diff

If the repository ships a PR template, agents must fill it in.

Agents must report the PR URL to the user when done.

## Commit Rules

Commit messages must follow the **gitmoji** convention (https://gitmoji.dev).

Agents should load the `gitmoji` skill to generate commit messages.

Format:

```
<gitmoji> <type>: <summary>
```

Examples:

* `:bug: fix: handle empty volume id in NodePublish`
* `:sparkles: feat: add syncthing health check endpoint`
* `:recycle: refactor: extract folder mapping helper`

Rules:

* one gitmoji per commit, chosen for the intent of the change
* summary in lowercase, imperative, no trailing period
* keep commits scoped to a single logical change

---

# Agent Execution Protocol

Agents must follow this **exact sequence** when implementing changes.

### Step 0 — Create a working branch

Create a branch from `main` (unless instructed otherwise) before making any edits.

See **GitHub Workflow**.

---

### Step 1 — Understand the change

Identify:

* affected modules
* required dependencies
* required tests

Agents must **avoid speculative edits**.

---

### Step 2 — Implement minimal code change

Changes should:

* affect the smallest possible number of files
* preserve existing architecture
* follow coding conventions

---

### Step 3 — Format code

```
make fmt
```

---

### Step 4 — Run tests

```
make test
```

All tests must pass.

---

### Step 5 — Verify container build

```
make docker-build
```

---

### Step 6 — Verify Makefile coverage

If the change introduces:

* new workflows
* new commands
* new build steps

then the **Makefile must be updated**.

---

### Step 7 — Push and open a pull request

```
git push -u origin <branch>
gh pr create --base main --title "<type>: <summary>" --body "<description>"
```

See **Pull Request Rules** for description requirements.

Report the PR URL to the user.

---

# Patch Size Policy

Agents should prefer **small patches**.

Guidelines:

| Change Type | Expected Size |
| ----------- | ------------- |
| bug fix     | 1–3 files     |
| feature     | <10 files     |
| refactor    | limited scope |

Large refactors should be split into multiple commits.

---

# Prohibited Actions

Agents must **not**:

* commit unformatted code
* call go commands directly
* bypass Makefile workflows
* add large frameworks without justification
* work directly on `main`
* push a branch without opening a PR via `gh`
* write commit messages without a gitmoji

---

# Repository Invariants

The following must **always remain true**:

* project compiles
* tests pass
* formatting passes
* Docker image builds
* Makefile commands work

If any invariant breaks, the change is invalid.

---

# Quick Command Reference

Run tests:

```
make test
```

Format code:

```
make fmt
```

Build container:

```
make docker-build
```

Start a task:

```
git checkout main && git pull && git checkout -b <type>/<short-description>
```

Open a pull request:

```
git push -u origin <branch>
gh pr create --base main --title "<type>: <summary>" --body "<description>"
```

---

# Final Principle

Agents must behave like **conservative maintainers**.

Prefer:

* small changes
* predictable behavior
* repository stability

over clever or complex solutions.
