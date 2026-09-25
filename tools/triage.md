# GitHub issue triage in T3 Code

T3 Code runs the triage itself. A dispatcher thread checks for new GitHub
issues on a schedule and launches one thread per issue, each in its own worktree.
The agent audits the issue, proposes a fix, implements it and leaves everything for
the maintainer to review. Nothing is pushed and no pull request is opened.

No external script, timer or token is involved: `schedule_task` is the trigger and
`t3_thread_launch` creates the per-issue threads.

## Set it up once

1. In T3 Code, create a thread in the keryx project with the workspace set to
   **Local** (the project checkout) and permissions **Full access**. Title it
   "Issue triage dispatcher". `t3_thread_launch` requires a full-access caller.
2. Make sure the labels exist:

   ```sh
   gh label create "triage:wip" --color FBCA04 --description "T3 Code triage in progress" --force
   gh label create "triage:done" --color 0E8A16 --description "T3 Code triage ready for review" --force
   gh label create "triage:wontfix" --color FFFFFF --description "T3 Code audit found nothing to change" --force
   ```

3. Import the repository's worktree setup action once: **Settings → Projects &
   threads → Actions** (with the keryx project selected) → import `t3.json`. It
   runs `make app-install` in every new worktree so the per-issue agent can run
   the test suites.
4. Send the dispatcher thread this message:

   > Schedule a recurring task every 30 minutes, bound to this thread, that runs
   > the playbook in `tools/triage.md`.

   The agent calls `schedule_task` and reports the cadence and the next run. The
   scheduled runs post back into the dispatcher thread; `t3_thread_launch` then
   creates the per-issue threads.

That is the whole setup. The schedule fires while the T3 Code server runs: keep
the app open, or install the background service with `t3 service install` on the
machine that should triage with the app closed.

## For the dispatcher agent

You run on a schedule inside the dispatcher thread. For every run:

1. List the open issues:
   `gh issue list --repo v1b3coder/keryx --state open --limit 100 --json number,title,labels`.
2. Skip every issue that already carries a `triage:wip`, `triage:done` or
   `triage:wontfix` label. Those are claimed or finished; never dispatch them
   twice.
3. For each remaining issue, in order:

   - Claim it first: `gh issue edit <number> --repo v1b3coder/keryx --add-label triage:wip`.
   - Launch one top-level thread with a new worktree via `t3_thread_launch`:
     - `title`: `Issue #<number>: <title>`
     - `workspaceStrategy`: `{"type":"worktree","baseRef":"main","branch":"triage/issue-<number>"}`
     - `message`: the per-issue prompt below.
   - If the launch fails, remove the claim again
     (`gh issue edit <number> --remove-label triage:wip`) and report it. A
     launch has no retry key: inspect `t3_thread_list` before retrying, because
     the thread may already exist.
4. Finish with one line per issue: number, thread id and status.

Keep the dispatcher context small: it only reads issues and launches threads, it
never reads the code or implements anything itself.

### The per-issue prompt

> Audit this issue against the codebase, propose a fix and implement it on this
> thread's branch. Follow the "For the per-issue agent" section of
> `tools/triage.md`. Do not push and do not open a pull request: commit the
> change and leave the findings in the conversation for review.

## For the per-issue agent

You work in one top-level thread with its own worktree, created from `main`.

1. Read the issue and its comments:
   `gh issue view <number> --repo v1b3coder/keryx --comments`.
2. Audit before writing code. Find the root cause or decide the request does not
   make sense. Check `spec/` when the issue touches protocol behavior and
   `AGENTS.md` for the project rules.
3. Put the audit and the proposal in the conversation before you start editing:
   what you found, what you propose, which files it touches, what the risks are.
4. Implement the change on this branch. Keep the diff focused on the issue; do not
   refactor unrelated code.
5. Run the tests that cover the change: `make sdk-test`, `make relay-test` or
   `make app-test`; run `make verify` when the change crosses components. Do not
   deploy anything.
6. Commit with a Conventional Commit message that references the issue, for example
   `fix(app): ... (#123)`. Do not push and do not open a pull request.
7. Label the issue and finish with a summary:
   - `gh issue edit <number> --repo v1b3coder/keryx --add-label triage:done`,
   - summarize what the audit found, what changed, which tests ran, what is
     uncertain and what needs the maintainer's decision.

If the audit finds nothing worth changing, stop after step 3, label the issue
`triage:wontfix` and explain why in the conversation. Do not implement a change
you would not defend in review.

## For the maintainer

- Open T3 Code: each dispatched issue is a thread with its own worktree.
- Read the conversation (audit, proposal, summary) and the diff against `main`.
- Push and merge the branch yourself; the agent never does.
- Discard by deleting the thread (T3 Code cleans up the worktree) and closing the
  issue, or remove the `triage:wip` label to have the dispatcher pick the issue up
  again.
- Manage the schedule from the dispatcher thread with `list_scheduled_tasks`,
  `update_scheduled_task` (pause with `enabled=false`) and
  `delete_scheduled_task`.
