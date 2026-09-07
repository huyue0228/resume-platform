# AGENTS.md

This file gives Codex working context for this repository. The project was originally bootstrapped with Claude Code; keep `CLAUDE.md` as historical context, but prefer this file for Codex sessions.

## Project Overview

This is an intelligent resume screening system for campus recruitment. The core candidate workflow is: deduplication and volunteer-order resolution -> Django Policy Gate -> fixed business references -> Go Agent Kernel screening -> Django validation, assignment, and dispatch. School classification, job-demand entry, and department-contact maintenance provide prerequisite data. New processing tasks do not expose selectable Rule/AI modes.

Tech stack:

- Backend: Django 4.2 + Django REST Framework + Celery, default local mode uses SQLite and eager Celery.
- Frontend: Vite + React 18 + Ant Design Pro, JavaScript `.jsx` only.
- Docs: `docs/` contains four design documents: `需求描述.md`, `后端设计.md`, `数据库设计.md`, `前端设计.md`.

## Docs Workflow

Do not automatically synchronize the four design documents under `docs/`. Update documentation only when the user explicitly asks for documentation work. Do not automatically stage, commit, or push code or documentation.

## Common Commands

Backend, from `backend/`:

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
python manage.py migrate
python manage.py seed_base
python manage.py gen_sample
python manage.py load_sample
python manage.py runserver 8000
```

Frontend, from `frontend/`:

```bash
npm install
npm run dev
npm run lint
npm run build
```

Full stack, optional:

```bash
docker compose build
docker compose --profile init run --rm init
docker compose up
```

Compose uses project Dockerfiles now: backend/worker share `smart-resume-filter-backend:${APP_VERSION:-latest}` based on Python 3.12.3, frontend uses a built React bundle served by Nginx, and db/redis are wrapped as project images for offline deployment. See README for server deployment details.

For an amd64 offline release and external-drive handoff, use the project Skill at `skills/smart-resume-offline-release/SKILL.md`; its single entry point builds, verifies, packages, and copies the release without modifying the current Git worktree.

## Verification

Use focused verification for the files changed:

- Backend model/API/pipeline changes: run relevant Django checks or commands from `backend/`, at minimum `python manage.py check`. If migrations are involved, run `python manage.py makemigrations accounts core`.
- Frontend changes: run `npm run lint` and `npm run build` from `frontend/` when practical.
- End-to-end local path: migrate, `seed_base`, `gen_sample`, `load_sample`, run backend on `8000`, frontend on `5173`, then log in with seeded RBAC accounts.
- Backend tests exist for ingestion, pipeline, API, AI decision retry, and RBAC/data-scope behavior. Prefer focused tests plus `python manage.py check`.

## Architecture Rules

- Business logic must not depend directly on Excel column names. External files are adapted through `backend/apps/ingestion/` into domain models in `backend/apps/core/models.py`.
- The data model has no batch concept. Use `imported_at` time tags for filtering; incremental imports should query by business keys in the service layer, update a single match, create when absent, and reject ambiguous duplicates.
- `Candidate` is the person entity and is merged by normalized name + normalized phone in the application layer; missing-phone candidates are skipped.
- `Resume` is the application entity and is matched by `apply_id` in the application layer; one candidate can have many resumes.
- Keep comments and user-facing docs mainly in Chinese, following the existing style.

## Pipeline Notes

- Entry point: `backend/apps/pipeline/runner.py`; API submits one mode-free `ProcessingRun` through `create_run()` and workers execute it through `execute_run()`.
- `all` and normal resume processing run `step1`, `step2`, `step3`, `step4` in order: deduplication, Policy Gate, frozen business references, then Agent evaluation.
- Step implementations live in `backend/apps/pipeline/services/`.
- Django owns deterministic admission/capacity/reference policy, final writes, and audit. Go source is in the independent sibling repository `../resume-agent-kernel`; platform builds consume a released image, not that source path. `backend/resume_contracts` is a versioned contract bundle. See `REPOSITORIES.md` for isolated mock development. The only production runtime model connection source remains the `settings.manage_ai_connection`-protected system settings page.
- Agent screening evaluates only the current effective volunteer and a frozen job/department reference. It never skips volunteer order or admission policy and never falls back to the historical Rule implementation.
- Agent failures, timeouts, parse failures, invalid output, missing references, and guardrail blocks should be recorded for HR handling. HR chooses retry Agent, manual assignment, or archive handling.
- Frontend submits one `/api/pipeline/run/` request with a `step` and optional `scope` via `frontend/src/components/useProcessRunner.jsx`; the backend creates one version-pinned Agent run and executes it through the sequential Celery orchestration. New tasks are accepted only while the current model connection and Agent Kernel are ready.

## API Notes

- Main API code is under `backend/apps/api/`.
- Standard pagination lives in `backend/apps/api/pagination.py`; list APIs support `page_size` with a max of 500.
- Routes use DRF `DefaultRouter` for resumes, candidates, jobs, schools, departments, contacts, workflow attempts, agent decisions, and runs.
- Explicit endpoints include `auth/logout/`, `auth/w3/status/`, `auth/w3/start/`, `auth/w3/callback/`, `auth/w3/complete/`, `me/`, `permissions/`, `import/`, and `pipeline/run/`. Import undo is removed; candidate bulk deletion is `/api/candidates/bulk-delete/`.
- `AssignmentAttemptViewSet` has a `dispatch_welink` method with `url_path="dispatch"`. Do not rename it to `dispatch`, because that would override DRF ViewSet dispatch.
- Resume export and preview helpers return Django `HttpResponse`, not DRF `Response`. Keep zip headers such as `X-Export-Count` / `X-Export-Missing` and preview header `X-Resume-Filename` stable for the frontend.
- Candidate export, resume direct preview, assignment-attempt scoped preview, and visible-column list filters are covered in `backend/apps/api/tests.py`; keep those tests aligned when changing table columns or query params.
- API defaults to authenticated access. Production login is W3 OAuth2 only; W3 completion returns the existing DRF Token session. `/api/auth/login/` and `/admin/` are intentionally absent and both return 404. When `DEBUG=True` and W3 is not ready, use `python manage.py issue_dev_token --username <employee_no>` and let the login page validate that Token through `/api/me/` before storing it.
- `User` retains Django's `AbstractUser.password` column for framework compatibility, but every account must have an unusable password. User APIs reject any `password` field, contact imports and `seed_base` must call `set_unusable_password()`, and no UI or documentation should reintroduce default-password, initial-password, or reset-password flows.
- Contact imports automatically create/update interface-user accounts with `username = Contact.employee_no`, an unusable password, and the matching second/third-level contact role. W3 maps by employee number plus email.

## Frontend Notes

- Pages live in `frontend/src/pages/`.
- API wrappers live in `frontend/src/api/`.
- Layout and permission-code menu filtering live in `frontend/src/layouts/BasicLayout.jsx`.
- `RoleContext.jsx` holds the current token-backed user, `/api/me/` permissions, roles, contact binding, and data-scope helpers. Do not reintroduce demo role switching.
- Resume upload and manual processing do not expose a Rule/AI selector. `/api/import/` rejects `processing_mode`, and `/api/pipeline/run/` rejects `mode`/`modes`; historical attempt sources remain readable for audit.
- Import UI is decentralized through `frontend/src/components/ImportButton.jsx`; there is no standalone import page.
- Shared table header filters and resizable column wiring live in `frontend/src/components/DataTableControls.jsx` and `frontend/src/components/ResizableHeaderCell.jsx`; reuse them for dense data tables instead of rebuilding per page.
- PDF preview UI lives in `frontend/src/components/ResumePreview.jsx` and supports direct resume previews and assignment-attempt scoped previews.
- `SchoolsPage.jsx` exposes the current school name, label and province fields only. North/south judgment is calculated from province at runtime; do not add regional fields or a regional configuration page.

## Migration Gotchas

- The backend uses `AUTH_USER_MODEL = "accounts.User"`.
- When creating or changing models, run migrations with explicit app names:

```bash
python manage.py makemigrations accounts core
```

Running `makemigrations` without app names has previously missed changes.

## Generated / Local Files

Do not commit local demo data or generated artifacts:

- `backend/db.sqlite3`
- `backend/sample_data/`
- `backend/media/`
- `纪要.md`
