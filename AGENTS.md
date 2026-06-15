# Agent guidance for Kueue

## Operator-chaos shift-left validation (L2)

This repository integrates [operator-chaos](https://github.com/opendatahub-io/operator-chaos) at **maturity level L2**:

- **L1**: GitHub Actions workflow runs breaking-change detection on PRs that touch APIs, controllers, CRDs, or chaos assets.
- **L2**: A repo-local knowledge model and chaos experiment YAMLs validate upgrade state transitions via `simulate-upgrade --dry-run`.

### Local validation

Install the pinned CLI and validate offline:

```bash
make chaos-validate
```

This runs:

1. `operator-chaos validate --knowledge chaos/knowledge/kueue.yaml`
2. `operator-chaos preflight --knowledge chaos/knowledge/kueue.yaml --local`
3. `operator-chaos validate` for each file in `chaos/experiments/`

### CI workflow

The workflow at `.github/workflows/chaos-validate.yml` runs on PRs that modify:

- `apis/**`
- `pkg/controller/**`
- `pkg/webhooks/**`
- `config/components/crd/**`
- `config/rhoai/**`
- `chaos/**`

It validates the knowledge model, diffs knowledge and CRD schemas against the PR base, validates experiment YAMLs, and previews upgrade simulation with `--dry-run`. Breaking knowledge or CRD changes fail the check.

### Maintenance expectations

Update `chaos/knowledge/kueue.yaml` when any of the following change:

- RHOAI deployment topology in `config/rhoai/` (resource names, namespace, webhooks)
- Controller-managed resources or steady-state health signals
- Admission webhook names or paths in `config/components/webhook/manifests.yaml`

Update or add experiments in `chaos/experiments/` when new upgrade-relevant failure modes should be covered (for example config drift, webhook disruption, or CRD mutation during upgrades).

Regenerate CRDs with `make manifests` after API type changes under `apis/`.

### Future maturity (not yet adopted)

- **L3**: ChaosClient SDK tests in envtest/integration suites
- **L4**: Upgrade playbook YAML and OLM channel-hop simulation

See the operator-chaos repository for experiment authoring and CLI reference.
