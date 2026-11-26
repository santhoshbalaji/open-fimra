# State-approved aggregate exchange

[Version 1 JSON Schema](extract.schema.json) describes the signed incident return and its separate preparedness and grants file. The [CSV layout](csv-layout.md) is for recipients who need a table. Use the signed JSON for authentication. The [examples](examples/) use a fictional state and include an original, a correction, its preparedness file, and validation receipts.

## Identity and revisions

Agree on the state code, the reporting unit codes and their code system before configuring the producing workspace. Each unit code must be unique among expected units on a reporting day. The producer is the configured state authority. Its root workspace administrator signs under the `state_admin` role. The file identifies the role and authority code.

The window includes both named reporting days. Each day is named for the local date on which it closes. The timestamps span from the previous day's cutoff to the last day's cutoff, including the start and excluding the end. A window must fit inside one reporting season. The season is identified by its start and end dates. A reporting unit's boundary version is the version frozen in its return, or the applicable version at cutoff if the return lacks one. An absent version is JSON null.

The state and window identify one reporting series. Treat `kind` as a separate dataset within that series. A correction reuses that state and window, increments the revision, and names the earlier extract in `supersedes`. Both files in an approval share the same ID and revision. When receiving a correction, replace the earlier figures for the same kind. Keep the earlier artifact for history. Reject conflicting authorities, overlapping windows, and revision gaps. The workspace enforces these rules for its own producing authority.

## Coverage and counts

An approved explicit nil return contributes **zero**. **Not reported** means no approved record is available. **Incomplete** means some expected coverage is absent or some approved snapshots cover part of the day or predate the extract fields. A legacy nil report still carries an explicit zero. Where an older non-nil snapshot lacks extract facts, request a new approved daily revision. The expected and reported counts are unit-days, so a unit reporting on two days contributes twice to coverage.

Incident totals come from the latest approved daily revision for each expected unit-day. Missing and legacy unit-days contribute no assumed counts. If there are no usable unit-days, the file omits `totals`. An incomplete file with usable facts carries the known figures. Its coverage shows the gaps.

- **Detections.** `records` counts distinct source records and equals the sum of source and forest-class counts. `deduplicated` counts distinct linked observations using the detection links already established at ingestion, including the provider copies that share a workspace match. An observation matched to several reporting units appears once in the state totals. The unit-day field `overlappingDetectionRecords` deliberately shows overlapping memberships. Use it to check coverage without adding the rows into a state total.
- **Alerts.** `routed` counts unique alerts observed in the window and assigned to a producing unit. Every routed alert has a disposition from its approved snapshot. `awaiting_at_cutoff` is an end-of-window count that can include older alerts and sits outside the disposition sum. An alert shown to several units contributes one alert to the state.
- **Confirmed fires.** A case needs affirmative field verification of forest fire on a member alert, excluding prescribed burns and rejected outcomes. Count a newly confirmed case when that evidence first appears. Count active fire and closure backlog at the final cutoff. Controlled, extinguished and closed are separate counts of unique confirmed cases that reached each stage in the window. A case can appear in several stage counts. Existing incident merges effective before the cutoff count under the surviving case.
- **Operations.** Assistance requested counts recorded support requests. Assistance received is the unit's approved daily tally. Overdue cases and alerts are final-cutoff figures. Response times cover recorded transitions for the named stage. The denominator is measured plus unknown. `totalSeconds / measured` gives mean elapsed seconds where a duration is known. An estimate contributes to the unknown count.
- **Area.** A case contributes at most one approved state allocation, using the latest approved assessment for the assessment's original window. The allocation must carry this state's code and give the hectares within its boundary. This keeps each state responsible for its own affected hectares when a fire crosses a border. The unique total adds the land class, method, and provisional or final breakdowns. A mixed method means the approving authority reconciled field and satellite figures. Impact totals count cases with a vegetation, wildlife or property category explicitly approved for exchange. The file carries no narrative, people impact text, contact information, case references, geometry or evidence.
- **Preparedness and grants.** The separate file counts approved preparedness work, training and readiness records. Its quantities show how many records included an approved target, completed work count, number trained or expenditure in rupees. Each quantity has its own denominator and status. Legacy free-text plans and results contribute to record counts only. Keep quantities comparable by using work items as the unit. No incident or referral fields are included.

## Verify a file

Get the producer's public key through an authenticated channel you trust. Keep the association between the state code and each public key. The API serves the public key by ID. The file itself supplies no trusted key.

```sh
cd packages/state-extract
go run ./cmd/verify examples/trusted-public-key.json examples/incident-correction.json
```

Use the public key to verify the Ed25519 signature over the UTF-8 `payload` object with insignificant JSON whitespace removed. Keep its token order, number spelling and escapes as issued. The key ID is the first 32 hexadecimal characters of the public key's SHA-256 digest. The receipt records the SHA-256 digest of the signed payload, its kind, revision, ID, signing key, validation time and coverage result. A valid receipt can say incomplete or not reported. Receipt results apply to the signed revision they identify.

## Configure and rotate signing

Enable encrypted workspace settings with `FIMRA_SETTINGS_KEY`, then use **Reports → State return** to set the agreed codes and generate the first signing key. The private seed is saved in the encrypted workspace secret store. Only the public key is shared.

Use the state extract settings endpoint with `{"action":"rotate"}` to generate a fresh signing key. Send the new public key to recipients through their authenticated channel. Earlier public keys stay available so every saved revision can still be checked. Rolling over the encryption key is a separate operation. Set the new `FIMRA_SETTINGS_KEY`, keep the previous one in `FIMRA_SETTINGS_OLD_KEYS`, and run `make rotate-settings-key` before retiring it.

For a fictional demonstration, run `make demo-detections demo-state-extracts` with encrypted settings configured and open **Reports → State return**. The replay signs an original with a gap and a correction after another unit submits a nil return. Both revisions have receipts.
