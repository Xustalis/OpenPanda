# OpenPanda v0.1.x product direction

[简体中文](product.md)

**Tell Panda what you need done. It finds a device and agent that can do the work and brings back the result.**

## When to add Panda

If your current agent or harness handles the whole job, keep it. Panda is for people who already have several agents or devices but still do the handoffs themselves. Your laptop may have the code and conversation, while a build machine has the test environment. Both can do their part; you still have to collect and check the result.

Panda's choice is to **bring back cross-device work as one job, using agents you already have on machines you manage**. Where work runs can change. Its goal, permissions and artifacts need clear ownership. Scheduling and node relays do not require a Panda-hosted service. External model APIs and agent services still handle data according to their own policies.

## What v0.1.x prioritizes

1. **Define what done means.** Let users state the outcome and acceptance criteria. Respect an explicit device constraint. Otherwise, prefer the simplest eligible route rather than distributing work for its own sake.
2. **Dispatch and recover.** Select executors by tools, environment and authorization. Resume or retry only when safe. When a permission or capability is missing, or a step cannot safely replay, explain the blocker and identify the decision a person needs to make.
3. **Bring back the work.** Show actual execution location, steps, artifacts, verification and remaining work at the origin. For independent pass criteria, use `panda task add --accept` to supply checks the executor actually runs. Keep not run, not verified and failed verification distinct. Refreshes, reconnects and retries must not mix evidence from different attempts.

Desktop and mesh improvements are prioritized by how much they help that path. Satellites and larger heterogeneous networks are long-term directions, not v0.1.x deployment commitments.

## A task to judge the experience by

From a laptop, ask Panda to run a project's tests and bring back the failing cases and raw output without changing the source. Try it locally first, then name a paired build machine that has the required tools and an agent. Check the actual executor, output and verification in the original conversation. If the job cannot recover after an interruption, it should be clear what ran, what evidence is missing and who needs to act next.

This is an acceptance scenario for v0.1.x, not a new test report. See [project status](status.en.md) for verified paths, environments and limits, and the [execution guide](agent-execution.en.md) and [routing rules](routing-layers.md) for details.
