# Demo State operations tour

Use a fresh development database and the development realm imported from this
checkout. Run `make demo-replay`, then start the API and web app. All the
accounts below use the password `demo`. Select **Demo State**.

| Sign in as | Where to look |
|---|---|
| `state.admin` | Open **Settings → Duty roster** for the two West control-room shifts and the seasonal squad. Open **Monitor → Alerts** for the reserve transfer. Compare the alert's original lead, boundary version and current lead. The work barge alert shows **Outside jurisdiction**. |
| `duty.officer` | Open **Monitor → Incidents** and select the large ridge fire in Hill and Valley divisions. Its member alerts include a radio call, a public report, two VIIRS passes and an alert from the demo Forest Survey source. The radio alert's dispatch details show the request for help from Valley Division. |
| `division.delegate` or `squad.leader` | Open the ridge fire under **Monitor → Incidents** to see the case from the supporting crew's area. The squad leader can also see alerts assigned to Demo Wildlife Division. |
| `section.forester` | Open the Reed Section case under **Monitor → Incidents**. The team reached the site and found no fire. |
| `beat.guard` | Open the Beat 01 case under **Monitor → Incidents**. A washed-out bridge stopped the team from reaching the point. |
| `division.officer` | Open **Reports → Referrals and preparedness** for the authorisation linked to the training plot alert. Open the ridge fire's offence referral to see the restricted evidence reference. |
| `range.officer` or `beat.alternate` | Open **Reports → Field reports** for the ridge flash report. The alternate's report has a GPS point, a fictional site photo and separate capture and receipt times. |
| `circle.reviewer` | Open **Reports → Field reports**. The ridge damage estimate was returned for correction. Open the approved survey to see the revised area and the link to the original estimate. The offence referral shows the approved summary. |
| `community.caller` or `district.liaison` | Open the ridge fire under **Monitor → Incidents**. The caller can add a public report in Beat 05. The liaison has read access across the two ranges in the fictional district. |
| `river.officer` or `state.admin` | Open **Reports → Daily situation return** and select the reporting day printed by the replay. River Division filed an approved nil return. From the administrator's view, Lake Division shows a missing return. |
| `state.admin` | Open **Reports → State return** for the signed aggregate and its validation receipt. It shows reporting coverage, including a zero and a gap. The extract covers the closed reporting window. The later field sync remains visible in the field reports. |

On an existing shared worktree stack, the realm still has the accounts it was
originally imported with. The state administrator and original division
officer can inspect the replay there. Use a fresh development realm for the
extended cast. See [demo accounts](../seed/README.md#demo-accounts).
