# Linking backend and frontend work

Both repositories (`messenger-back`, `messenger-front`) are planned together in one GitHub Project and use the same labels and milestones.

## GitHub Project

One user-level project, **Messenger**, owned by the `myronsi` account so it can hold issues from both repositories. Both repositories are linked to it and every open issue from both is in it (Area and Release were set from the repository and the milestone). Auto-add does not fill custom fields, so the views below filter on the built-in **Milestone** field, which is always set from the issue. Enable **Auto-add to project** in the project's Workflows (one rule per repository) so new issues arrive without manual work; GitHub has no API for that, nor for views.

| Field | Values |
| --- | --- |
| Area | backend, frontend, contract, infra |
| Release | 0.5, 1.0, 1.1 (mirrors the milestone; optional, views use Milestone) |
| Status | Todo, In Progress, Done |
| Priority | P0, P1, P2 |

Views (created in the project UI):

1. **Board per release**: Board layout, filter `milestone:"0.5"` (one view each for 1.0 and 1.1), grouped by Status. It shows the backend and frontend issues of that release together.
2. **API changes**: Table layout, filter `label:api-change`, grouped by Milestone.

## Labels

`.github/labels.yml` is the source of truth and is the same in both repositories; `.github/workflows/labels.yml` applies it when the file changes on the default branch (or when run manually). Other labels are kept.

| Label | Use |
| --- | --- |
| `api-change` | Changes the API contract |
| `breaking-change` | Breaks existing clients or the previous release |
| `needs-backend` / `needs-frontend` | The other side has to change too |
| `security`, `bug`, `feature` | Kind of work |
| `epic` | Parent issue with sub-issues |

## Milestones

Both repositories have the same milestones: `0.5`, `1.0` and `1.1`. Every open issue belongs to one of them.

## Features that touch both sides

1. Create one parent issue in `messenger-back` (where the contract lives) with the label `epic`.
2. Add sub-issues for the contract, the backend and the frontend (the frontend one lives in `messenger-front`; sub-issues can be cross-repository).
3. Reference counterparts with full references such as `myronsi/messenger-front#12` and mark ordering with "blocked by" relationships (usually frontend blocked by backend, backend blocked by contract).
4. Open the matching PRs and cross-link them; the PR template asks for the contract version and the counterpart PR.

## Templates

- Issue form `.github/ISSUE_TEMPLATE/feature.yml`: asks for the counterpart issue and whether the API changes.
- `.github/pull_request_template.md`: asks whether the API contract changed, the contract version, the counterpart PR and migration notes.
