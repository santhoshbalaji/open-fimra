# CSV layout for version 1

Use UTF-8 and a comma separator. Quote fields containing commas, quotes or line breaks. Write dates as ISO dates and instants with an explicit UTC offset. Use a decimal point for numeric values. An empty value means absent, and the value `0` means a reported zero.

Write the incident and preparedness files separately. Repeat the document metadata on each row. Use this header.

```csv
schema_version,kind,extract_id,revision,supersedes,state_code,unit_code_system,approver_role,season_from,season_to,window_from,window_to,time_zone,cutoff,starts_at,ends_at,generated_at,source,key_id,payload_sha256,row_type,unit_code,day,boundary_version,metric,status,expected,reported,value
```

| Row type | Unit, day and boundary | Metric and value |
| --- | --- | --- |
| `coverage` | Empty | No metric or value. Copy the document coverage status and expected and reported unit-days. |
| `unit_day_overlapping` | Copy the unit code, closing day and boundary version | Copy the unit's status. Use `overlapping_detection_records` for the metric, with the count if present. These rows overlap. |
| `total` | Empty | One field per row from detections, alerts, incidents and operations. Use the section and JSON key separated by dots, such as `detections.deduplicated`. Copy the count and the document coverage status. |
| `response` | Empty | For each stage, write its total seconds, measured and unknown counts in separate rows using `response.<stage>.<field>`. Repeat the denominator as expected and the measured count as reported. |
| `area` | Empty | Use `area.hectares`, `area.breakdowns.<land>.<method>.<status>` or `area.impacts.<category>`. Repeat the area coverage status and expected and reported case counts. |
| `preparedness` | Empty | Write each count and each quantity as a separate row, such as `preparedness.target`. Counts use the companion status. Quantities carry their own status, expected and reported record counts, and numeric value when present. |

The CSV is a view of an authenticated document. Retain the original signed JSON and receipt for verification.
