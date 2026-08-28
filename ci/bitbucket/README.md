# Bitbucket adapter

> **Unproven.** Written against documentation, not exercised against a live
> instance. Treat it as a starting point, and please report what was wrong.

See [`../README.md`](../README.md) for what an adapter has to do. The pieces
that differ per host:

- how to check out the pull request *and* its merge base
- the API call that reports a commit status
- how to post `report.md` as a pull-request comment, and how to update that
  comment in place. This is the verdict channel — the agent reads the gate's
  answer by listing comments, and nothing fetches build artifacts
- whether pushes made with the CI token re-trigger the pipeline
