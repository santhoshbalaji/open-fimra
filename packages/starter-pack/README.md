# Starter pack

Default hierarchy presets, forms and workflow that a new installation can switch on in the first-run wizard (PLAN.md sections 5.4 and 7).

The hierarchy presets are in the core API for now, in [setup_presets.go](../../services/api/setup_presets.go), with English and Hindi labels. The workflow is the default workflow in the database. The forms are here.

## Forms

The forms use the [form format](../form-schema/SPEC.md). They are made up for the fictional Demo State, and departments use them as they are, change them or replace them.

| Form | File | Filled in at | Who fills it in |
|---|---|---|---|
| Control-room dispatch | [forms/control-room-dispatch.json](forms/control-room-dispatch.json) | Team sent, then Reached site (two stages) | The control room when it sends a team, then the team when it reaches the site |
| First report | [forms/first-report.json](forms/first-report.json) | First report | The team, once the fire is out |
| Final report | [forms/final-report.json](forms/final-report.json) | Final report | The team, with the range officer's signature, before an officer verifies the alert |
| Manual fire report | [forms/manual-fire-report.json](forms/manual-fire-report.json) | No step: saving it makes an alert at New | Anyone who takes a report of a fire from another source |
| Patrol log | [forms/patrol-log.json](forms/patrol-log.json) | Optional record | A patrol, whether or not it found a fire |
| Offence referral | [forms/offence-referral.json](forms/offence-referral.json) | Case referral | Staff with the scoped offence grant |
| Relief referral | [forms/relief-referral.json](forms/relief-referral.json) | Case referral | Staff with the scoped relief grant |
| Preparedness work | [forms/preparedness-work.json](forms/preparedness-work.json) | Standalone record | The person responsible for the work |
| Fire training | [forms/fire-training.json](forms/fire-training.json) | Standalone record | The person running the training |
| Readiness check | [forms/readiness-check.json](forms/readiness-check.json) | Standalone record | The person checking the crew or equipment |
| Prescribed burn | [Authorisation](forms/prescribed-burn-authorisation.json) and [completion](forms/prescribed-burn-completion.json) | Standalone record | The officer or crew responsible for the planned burn |
| After-action finding | [forms/after-action-finding.json](forms/after-action-finding.json) | One record per corrective action | The officer recording the finding and the person who owns the action |

[pack.json](pack.json) lists the forms in order with the workflow steps each one is assigned to. A form with stages is assigned at the step of each stage.

The manual fire report makes a detection at `fire_location`, observed at `reported_at`, which is matched and routed like a satellite detection. It has no photos, because photos are uploaded for an alert and this report makes the alert. See [manual fire reports](../../services/api/README.md#manual-fire-reports).

The patrol log is optional. A patrol that finds a fire can open a case with the manual fire report. The new referral and preparedness forms have no workflow step. See [referrals and preparedness](../../services/api/README.md#referrals-and-preparedness).

## Switching it on

The wizard's starter forms step, or the request `POST /api/v1/workspaces/{slug}/starter-pack`, adds each starter form that the workspace does not have yet, publishes it and assigns it to its steps. A form the workspace already has with the same key is left as it is,. The pack leaves occupied steps alone. The extended damage assessment can share Final report with the completion form. Turning it on again is safe. See the [starter pack endpoints](../../services/api/README.md#starter-pack).

## Go package

The core API embeds this folder through the Go module `github.com/santhoshbalaji/open-fimra/packages/starter-pack`. `starterpack.Forms()` returns the forms in order with their steps and exact text.

```bash
make starter-pack-test   # every form passes the Go validator and fits the default workflow
```

The drafts in [packages/form-schema/examples](../form-schema/examples) are format examples for the validators and the XLSForm converter. Change the forms here.

## Extended response workflow

The extended workflow adds these reports to the starter pack.

| Report | Step | What it records |
| --- | --- | --- |
| Fire intake report | Assigned | Source, reported location, team, threats and help needed |
| Control-room dispatch | Team sent, Reached site | Crew, instructions, dispatch time and arrival |
| Flash update | Flash update | What the team saw at this alert, threats, resources and help needed |
| Response update | Response update | Changes on site, work underway and next contact. File a new report at each check-in. |
| Action taken | Extinguished | What the team did, resources used and the watch plan |
| Damage assessment | Final report | Surveyed or estimated area, method, uncertainty, effects and photos |

Use **Revise this assessment** on the original damage assessment to start a
revision. Each report has its own evidence and review decision. The original
and its figures remain in the alert's reports.

Turn on the pack again to add missing forms to an extended workspace. When a
workspace with the original starter pack switches on the extended workflow,
the extra reports are added for new cases. This keeps existing form versions
and assignments available.
