# Repository Guidelines

## Project Structure & Module Organization
- `backend/`: Go service. `cmd/server` is the entrypoint, `internal/` contains handlers/services/repositories/server wiring, `ent/` holds Ent schemas and generated ORM code, `migrations/` stores DB migrations, and `internal/web/dist/` is the embedded frontend build output.
- `frontend/`: Vue 3 + TypeScript app. Main folders are `src/api`, `src/components`, `src/views`, `src/stores`, `src/composables`, `src/utils`, and test files in `src/**/__tests__`.
- `deploy/`: Docker and deployment assets (`docker-compose*.yml`, `.env.example`, `config.example.yaml`).
- `openspec/`: Spec-driven change docs (`changes/<id>/{proposal,design,tasks}.md`).
- `tools/`: Utility scripts (security/perf checks).

## Build, Test, and Development Commands
```bash
make build                           # Build backend + frontend
make test                            # Backend tests + frontend lint/typecheck
cd backend && make build             # Build backend binary
cd backend && make test-unit         # Go unit tests
cd backend && make test-integration  # Go integration tests
cd backend && make test              # go test ./... + golangci-lint
cd frontend && pnpm install --frozen-lockfile
cd frontend && pnpm dev              # Vite dev server
cd frontend && pnpm build            # Type-check + production build
cd frontend && pnpm test:run         # Vitest run
cd frontend && pnpm test:coverage    # Vitest + coverage report
python3 tools/secret_scan.py         # Secret scan
```

## Coding Style & Naming Conventions
- Go: format with `gofmt`; lint with `golangci-lint` (`backend/.golangci.yml`).
- Respect layering: `internal/service` and `internal/handler` must not import `internal/repository`, `gorm`, or `redis` directly (enforced by depguard).
- Frontend: Vue SFC + TypeScript, 2-space indentation, ESLint rules from `frontend/.eslintrc.cjs`.
- Naming: components use `PascalCase.vue`, composables use `useXxx.ts`, Go tests use `*_test.go`, frontend tests use `*.spec.ts`.

## Testing Guidelines
- Backend suites: `go test -tags=unit ./...`, `go test -tags=integration ./...`, and e2e where relevant.
- Frontend uses Vitest (`jsdom`); keep tests near modules (`__tests__`) or as `*.spec.ts`.
- Any changed code must include new or updated unit tests.
- Frontend coverage threshold is 85% global (statements/branches/functions/lines).
- Before opening a PR, run `make test` plus targeted tests for touched areas.

## Commit & Pull Request Guidelines
- Follow Conventional Commits: `feat(scope): ...`, `fix(scope): ...`, `chore(scope): ...`, `docs(scope): ...`.
- PRs should include a clear summary, linked issue/spec, commands run for verification, and screenshots/GIFs for UI changes.
- For behavior/API changes, add or update `openspec/changes/...` artifacts.
- If dependencies change, commit `frontend/pnpm-lock.yaml` in the same PR.

## Security & Configuration Tips
- Use `deploy/.env.example` and `deploy/config.example.yaml` as templates; do not commit real credentials.
- Set stable `JWT_SECRET`, `TOTP_ENCRYPTION_KEY`, and strong database passwords outside local dev.
