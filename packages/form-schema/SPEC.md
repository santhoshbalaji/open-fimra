# Open FIMRA form format, format 1

This document describes how Open FIMRA writes down a form: the questions, how they are grouped, when they are shown, what they check and what data they produce. The web form designer, the mobile app and the core API all read the same format.

A form is a JSON file. JSON is a common text format for data. [form.schema.json](form.schema.json) is the machine-readable description of the file, written as a JSON Schema (draft 2020-12). Some rules cannot be written in a JSON Schema, so the validators in this folder check those too. Section 14 lists every rule.

## 1. Words used here

| Word | Meaning |
|---|---|
| Form | One kind of report, such as the first report. It has an id. |
| Version | One fixed copy of a form. Each version has a number. |
| Item | Anything inside a section: a field, a group or a repeating group. |
| Field | One question with one answer, such as a date or a photo. |
| Section | A top-level part of a form, such as "The fire" or "Response". |
| Group | A sub-section inside a section or another group. |
| Repeating group | A group that can be filled in more than once, such as one entry per team. |
| Stage | A part of a form that is filled in at one step of the workflow. |
| Key | A short name for an item, a choice or a list, used by the computer. People never see it. |
| Text | Words that people see, given in one or more languages. |
| Condition | A test on the answers, such as "cause is lightning". |
| Calculation | A number worked out from other answers. |
| Layout | How the form looks: pages, widths and the kind of control for each question. |
| Submission | One filled-in copy of a form version. |
| Column | One value in the data a form produces. |

## 2. The form file

```json
{
  "formatVersion": 1,
  "id": "first-report",
  "version": 1,
  "title": { "en": "First report", "hi": "प्रारंभिक रिपोर्ट" },
  "sections": [
    {
      "key": "fire",
      "title": { "en": "The fire" },
      "items": [
        { "key": "reached_at", "type": "date-time", "label": { "en": "Team reached the site at" }, "required": true }
      ]
    }
  ]
}
```

| Property | Needed | What it holds |
|---|---|---|
| `$schema` | No | A link to the schema, so editors can help while you type. Ignored otherwise. |
| `formatVersion` | Yes | Always `1` for this format. |
| `id` | Yes | The form's id: small letters and digits with single hyphens between words, such as `first-report`. |
| `version` | Yes | The version number, a whole number from 1. |
| `title` | Yes | The form's name, as text. |
| `description` | No | A longer explanation, as text. |
| `languages` | No | The languages every text must have. See section 4. |
| `stages` | No | The workflow steps the form is filled in at. See section 7. |
| `lists` | No | Lists of choices shared by several fields. See section 9. |
| `sections` | Yes | The sections, in order. There must be at least one. |
| `layout` | No | How the form looks. See section 12. |

No other properties are allowed anywhere in the file, so a misspelt property name is caught.

## 3. Versions and ids

A form version is identified by its workspace, its `id` and its `version` number. In writing, we say "first-report version 3".

- While a version is a draft, it can change freely.
- When a version is published, it is frozen. Its file never changes again. The server keeps the exact text it published and refuses any change to it.
- To change a published form, publish a new version with the next number. Older versions stay as they were.
- Every submission records the id and version it used, so an old report always shows with the form it was filled in on.
- Keep each field key's meaning the same from one version to the next. If a question changes meaning, give it a new key. Then charts across versions stay correct.

The file does not say whether it is a draft or published. The server records that.

## 4. Text and languages

Everything people read is a text: an object with one entry per language.

```json
{ "en": "Fire location", "hi": "आग का स्थान" }
```

- English (`en`) is always needed. The English text is used when a translation is missing.
- Other languages use their language code, such as `hi` for Hindi or `ur` for Urdu. A code is two or three small letters, optionally followed by parts such as `-IN`.
- No text may be empty.

If the form lists `languages`, such as `["en", "hi"]`, then every text in the form must have every listed language, and no text may have a language that is not listed. The list must include `en`. Without `languages`, any mix of translations is allowed.

## 5. Keys

Items, choices, lists, stages and layout pages have keys.

- An item, list, stage or page key starts with a small letter and uses only small letters, digits and underscores, such as `reached_at`. It has at most 40 characters.
- A choice key uses small letters, digits, underscores and hyphens, such as `field-burning`.
- Every item key is unique across the whole form, including items in other sections, groups and repeating groups. A field's key is also its column name in the data, so this keeps column names unique.
- Choice keys are unique within their field or list.

## 6. Sections, groups and repeating groups

A form has sections. Each section has a `key`, a `title`, an optional `description`, an optional `stage`, an optional `showWhen` condition and a list of `items`.

Each item has a `type`. Two types hold other items:

- **`group`**: a sub-section. It has a `key`, a `title`, an optional `description`, an optional `showWhen` and its own `items`. Groups can hold groups.
- **`repeat`**: a repeating group. It has the same properties as a group, plus `itemLabel` (the name of one entry, such as "Team"), `minRepeats` and `maxRepeats`. A repeating group can hold groups and other repeating groups.

All other types are fields (section 8).

## 7. Stages

Some forms are filled in over several workflow steps. For example, the control room fills in the dispatch part when it sends a team, and the team fills in the arrival part when it reaches the site.

```json
"stages": [
  { "key": "dispatch", "title": { "en": "Team sent" }, "workflowStep": "team_sent" },
  { "key": "arrival", "title": { "en": "Reached site" }, "workflowStep": "reached_site" }
]
```

- Each stage names the workflow step it belongs to, such as `team_sent` from the default workflow.
- When a form has stages, every section names its stage with `"stage": "dispatch"`. Every stage is used by at least one section.
- Two stages cannot use the same workflow step.
- Stages run in the order listed. A question may depend on answers from its own stage or an earlier one, never on a later one.
- Answers from earlier stages are kept and shown read-only in later stages.

A form without stages is filled in at one step. Which step that is, and which parts of the hierarchy use the form, is set when the form is assigned, not in the file.

## 8. Fields

Every field has these properties:

| Property | Needed | What it does |
|---|---|---|
| `key` | Yes | The field's key and column name. |
| `type` | Yes | One of the types below. |
| `label` | Yes | The question, as text. |
| `hint` | No | Extra help shown under the question. |
| `required` | No | `true` if the question must be answered when it is shown. |
| `showWhen` | No | Show the question only when this condition is true. |
| `requireWhen` | No | The question must be answered when this condition is true. Use either this or `required`, not both. |

Calculated fields and values from the alert are filled in by the system, so they cannot have `required` or `requireWhen`.

A hidden question is never required, and its answer is not kept. When a section or group is hidden, everything inside it is hidden too.

| Type | What it is | Extra properties |
|---|---|---|
| `text` | Words typed in | `minLength`, `maxLength`, `pattern`, `patternHint` |
| `number` | A number with an optional unit | `unit`, `decimals`, `min`, `max` |
| `date` | A day | `notInFuture` |
| `time` | A time of day | none |
| `date-time` | A day and time | `notInFuture` |
| `single-choice` | Pick one choice | `choices` or `list`, `parent` |
| `multiple-choice` | Pick any number of choices | `choices` or `list`, `parent`, `minSelected`, `maxSelected` |
| `point` | A position from the phone's GPS (satellite positioning) or picked on the map | `maxAccuracyMetres` |
| `track` | A line recorded while walking | `maxAccuracyMetres` |
| `polygon` | An area drawn on the map or walked around. Its area is computed | `maxAccuracyMetres` |
| `photo` | One or more photos | `minCount`, `maxCount` (up to 20) |
| `signature` | A signature drawn on the screen | none |
| `file` | One or more files | `minCount`, `maxCount`, `maxSizeMegabytes`, `accept` (file endings such as `.pdf`) |
| `calculated` | A number worked out from other answers | `calculate` (required), `unit`, `decimals` |
| `alert-value` | A value copied from the alert | `source` (required) |

What the extra properties mean:

- `minLength`, `maxLength`: the fewest and most characters in a text answer.
- `pattern`: the answer must match this regular expression as a whole. Patterns must work the same in Go and in browsers, so a few features are not allowed: anything starting `(?` (such as look-ahead), back-references such as `\1`, the escapes `\k \A \z \Q \E \C \p \P \G \Z`, character classes such as `[[:alpha:]]`, empty classes `[]`, and loose `{`, `}` or `]` characters. Write `\{` for a real brace.
- `patternHint`: text that explains the pattern to people, such as "Ten digits".
- `unit`: a short unit name, such as `ha`, `km`, `people` or `rupees`.
- `decimals`: how many decimal places to keep, from 0 to 6. With 0, the answer is a whole number.
- `min`, `max`: the smallest and largest allowed number.
- `notInFuture`: the date cannot be later than now.
- `maxAccuracyMetres`: the phone refuses a position less accurate than this.
- `minSelected`, `maxSelected`, `minCount`, `maxCount`, `minRepeats`, `maxRepeats`: the fewest and most choices, files or entries. A smaller limit can never be more than the larger one.

### Values from the alert

An `alert-value` field shows a value from the alert the form is filled in for. People cannot change it.

| `source` | Value |
|---|---|
| `alert-id` | The alert's number, as text |
| `observed-at` | When the satellite saw the heat, as a date and time |
| `raised-at` | When the alert was raised, as a date and time |
| `location` | The detection's position, as a point |
| `unit` | The responsible unit, such as a beat, as text |
| `forest-class` | Inside, near the edge or outside the forest, as text |
| `source` | The satellite product, as text |
| `confidence` | The source's confidence, as text |
| `fire-power` | The fire radiative power in megawatts (`MW`), as a number |

## 9. Choices, lists and cascading lists

A choice has a `key`, a `label` and, in a cascading list, a `parent`.

A choice field gives its choices in one of two ways:

- `choices`: a list of choices inside the field.
- `list`: the key of a shared list in the form's `lists`. A shared list has a `key` and `choices`.

A field needs exactly one of the two.

A **cascading list** narrows its choices by the answer to another field. The field names that field in `parent`. The parent must be a `single-choice` field. Every choice in the child then names, in its own `parent`, a choice key of the parent field. Only the choices whose parent matches the answer are offered.

```json
{ "key": "cause_group", "type": "single-choice", "label": { "en": "Kind of cause" }, "list": "cause_groups" },
{ "key": "cause", "type": "single-choice", "label": { "en": "Likely cause" }, "list": "causes", "parent": "cause_group" }
```

with the list `causes` holding choices such as `{ "key": "lightning", "label": { "en": "Lightning" }, "parent": "natural" }`.

A choice may only have a `parent` when its field has one.

## 10. Conditions

`showWhen` and `requireWhen` hold a condition. Sections, groups and repeating groups can have `showWhen` too. Every condition has an `op`:

| `op` | True when | Other properties |
|---|---|---|
| `answered` | the field has an answer | `field` |
| `equals` | the answer is the value | `field`, `value` |
| `not-equals` | the answer is not the value | `field`, `value` |
| `more-than` | the answer is more than the value | `field`, `value` |
| `at-least` | the answer is the value or more | `field`, `value` |
| `less-than` | the answer is less than the value | `field`, `value` |
| `at-most` | the answer is the value or less | `field`, `value` |
| `includes` | a multiple choice answer includes the value | `field`, `value` |
| `all` | every condition in the list is true | `conditions` |
| `any` | at least one condition in the list is true | `conditions` |
| `not` | the condition is false | `condition` |

```json
"showWhen": {
  "op": "any",
  "conditions": [
    { "op": "equals", "field": "cause", "value": "human-other" },
    { "op": "equals", "field": "cause", "value": "natural-other" }
  ]
}
```

Which tests suit which fields:

| Field | Tests | The value is |
|---|---|---|
| text, or an alert value that is text | `equals`, `not-equals` | text |
| number, calculated, or `fire-power` | `equals`, `not-equals`, `more-than`, `at-least`, `less-than`, `at-most` | a number |
| date | the same | a date such as `2026-03-15` |
| time | the same | a time such as `14:30` |
| date and time, `observed-at` or `raised-at` | the same | a date and time with its offset from universal time, such as `2026-03-15T14:30+05:30` |
| single choice | `equals`, `not-equals` | one of its choice keys |
| multiple choice | `includes` | one of its choice keys |
| any field | `answered` | no value |

An unanswered field makes every test except `answered` false.

A condition can only use a field, not a section or group. It can use fields in the same repeating group entry, or outside any repeating group. It cannot use a field inside a repeating group it is not itself in, because that field has many answers. Use `sum` or `count` in a calculated field instead. A section or group cannot depend on a field inside itself.

## 11. Calculations

A calculated field's `calculate` holds a calculation. Every calculation gives a number and has an `op`:

| `op` | Gives | Other properties |
|---|---|---|
| `number` | a fixed number | `value` |
| `field` | the answer to a number field, another calculated field or the `fire-power` alert value | `field` |
| `add` | the total of two or more calculations | `args` |
| `multiply` | the product of two or more calculations | `args` |
| `subtract` | the first calculation minus the second | `args` (exactly two) |
| `divide` | the first calculation divided by the second | `args` (exactly two) |
| `sum` | the total of a number field over every entry of a repeating group | `group`, `field` |
| `count` | the number of entries in a repeating group | `group` |
| `area` | the area of a `polygon` field, in hectares | `field` |
| `length` | the length of a `track` field, in kilometres | `field` |
| `hours-between` | the hours from one date and time to another | `start`, `end` |

```json
{ "key": "total_staff", "type": "calculated", "label": { "en": "People on site" }, "unit": "people",
  "calculate": { "op": "sum", "group": "teams", "field": "members" } }
```

- `sum` adds a field that sits in the named repeating group itself, not in a repeating group further inside it.
- `sum` and `count` cannot be used from inside the group they add up.
- `hours-between` takes date and time fields, or the alert's `observed-at` and `raised-at`.
- A calculation that cannot be worked out, such as one with an unanswered field or a division by zero, has no answer.

The phone works calculations out to show them. The server works them out again when the submission arrives and keeps its own result, and it fills in values from the alert itself.

### No loops

A field cannot depend on itself, directly or through other fields. A field depends on the fields in its `showWhen`, in its `calculate` and in its `parent`. For example, `first` shown when `second` is answered, and `second` shown when `first` is answered, is a loop. `requireWhen` is not counted, because it never hides a question.

## 12. Layout

The `layout` says how the form looks. It never changes the data a form collects: two versions that differ only in layout produce the same columns.

```json
"layout": {
  "pages": [
    { "key": "send", "title": { "en": "Send a team" }, "sections": ["alert", "dispatch"] },
    { "key": "arrive", "sections": ["arrival"] }
  ],
  "items": [
    { "item": "vehicle", "widget": "buttons" },
    { "item": "team_name", "width": "half" },
    { "item": "alert", "collapsed": true }
  ]
}
```

- `pages` splits the sections over pages, in order. Without it, each section is its own page. With it, every section is on exactly one page. In a form with stages, a page holds sections from one stage only.
- `items` gives display settings for single items. Each item is listed at most once.
  - `widget` picks the control: `line` or `paragraph` for text, `input` or `stepper` for numbers, `radio`, `dropdown` or `buttons` for single choice, and `checkboxes` or `buttons` for multiple choice.
  - `width` is `full` or `half`. Two half-width items in a row sit side by side on wide screens.
  - `collapsed` starts a section, group or repeating group folded away.

## 13. The data a form produces

### A submission

A submission holds the answers by field key. Groups do not add any nesting, because keys are unique across the form. Each repeating group holds a list of entries.

```json
{
  "form": "first-report",
  "version": 1,
  "values": {
    "reached_at": "2026-03-15T14:30:00+05:30",
    "cause_group": "natural",
    "cause": "lightning",
    "vegetation": ["grass", "leaf-litter"],
    "teams": [
      { "team_name": "Demo Team A", "members": 6 },
      { "team_name": "Demo Team B", "members": 4 }
    ]
  }
}
```

Unanswered and hidden fields are left out. Each answer has the form given by its value type:

| Value type | Fields | Written as |
|---|---|---|
| `text` | text, and text alert values | text |
| `number` | number, calculated, `fire-power` | a number |
| `date` | date | text such as `2026-03-15` |
| `time` | time | text such as `14:30` |
| `date-time` | date-time, `observed-at`, `raised-at` | text with an offset from universal time, such as `2026-03-15T14:30:00+05:30` |
| `choice` | single choice | the choice key |
| `choices` | multiple choice | a list of choice keys |
| `point` | point, `location` | a GeoJSON Point in longitude and latitude, in the world coordinates that GPS uses (WGS 84) |
| `line` | track | a GeoJSON LineString |
| `polygon` | polygon | a GeoJSON Polygon |
| `attachment` | signature | the attachment's id |
| `attachments` | photo, file | a list of attachment ids |

GeoJSON is the common JSON format for map shapes. Attachments are stored and synced separately from the answers, and are never overwritten.

### Columns and the flat view

Each published version gets flat database views, so its answers can be charted and exported like ordinary columns. The validators list the columns with `Columns` in Go and `formColumns` in TypeScript. Each column has:

| Property | Meaning |
|---|---|
| `name` | The column name. |
| `field` | The key of the field it comes from. |
| `valueType` | One of the value types above. |
| `unit` | The unit, if the value has one. |
| `repeat` | The key of the innermost repeating group the field is in. Left out for fields outside any repeating group. |

- Every field gives one column named after its key.
- A `polygon` field also gives `<key>_area_ha`, its area in hectares, and a `track` field gives `<key>_length_km`, its length in kilometres. The server computes both from the shape. These names must not clash with any item key.
- Columns without `repeat` form one row per submission. Each repeating group forms its own view with one row per entry, linked to its submission and, for a repeating group inside another, to the outer entry.
- Sections, groups and layout give no columns.

The columns of a published version never change, because the version never changes. [examples/columns](examples/columns) holds the expected columns for each example form.

## 14. Checking a form

The validators check a form in two steps. First they apply [form.schema.json](form.schema.json). If that finds any problem, they stop there. Otherwise they check the rules below, in this order:

1. `languages` lists no language twice and includes `en`.
2. Stage keys are unique, and no two stages use the same workflow step.
3. List keys are unique, and choice keys are unique within each list.
4. Item keys are unique across the form.
5. Each item, in the order it appears in the file:
   1. a section names a stage that exists, and names one when the form has stages;
   2. no smaller limit is more than its larger one;
   3. a pattern uses only the allowed features and is a valid regular expression;
   4. a choice field has exactly one of `choices` and `list`, its list exists, its choice keys are unique, and its cascading parents are valid;
   5. a field does not have both `required` and `requireWhen`;
   6. every field used in `showWhen`, `requireWhen` and `calculate` exists, is a field, can be used from that place (section 10), is not filled in at a later stage and suits the test or calculation, and every compared value suits the field.
6. The extra area and length columns do not clash with other keys.
7. Nothing depends on itself.
8. Every stage is used.
9. The layout names only items and sections that exist, lists each once, gives each section one page, keeps a page to one stage, picks widgets that suit their fields and folds away only sections and groups.
10. Every text has every listed language, and no other.

Each problem gives one plain-English sentence. Problems found by the schema start with where the problem is, such as `sections[1].items[0].label is missing "en".` Places are counted from 0. Problems found by the rules name the item by its key, such as `Field "cause": showWhen refers to "fire_tpye", which does not exist.` The validators report every problem they find, in a fixed order.

### Two validators, one set of messages

| Validator | Where | Use |
|---|---|---|
| Go | this folder, module `github.com/santhoshbalaji/open-fimra/packages/form-schema` | `formschema.Validate(data)` returns the problems; `formschema.Columns(data)` returns the columns |
| TypeScript | [ts/form-schema.ts](ts/form-schema.ts) | `validateFormText(text)` or `validateForm(value)` returns the problems; `formColumns(value)` returns the columns |

Both read the same [form.schema.json](form.schema.json): Go embeds it when the package is built, and TypeScript imports it. Both understand only the JSON Schema keywords the schema uses, and refuse to load a schema with any other keyword. That way neither can drift from the other without a test failing. The TypeScript module has no dependencies.

Both validators run the shared test cases in [examples](examples):

- Every file directly in `examples/` is a valid form. Both validators must find no problems, and their columns must match the file of the same name in `examples/columns/`.
- Every file in `examples/invalid/` holds `expected`, a message, and `form`, a form broken in exactly one way. Both validators must report exactly that one message.

The examples include a draft of each starter-pack form: control-room dispatch, first report, final report, patrol log and manual fire report. They are made up for the fictional Demo State. The finished forms that a workspace switches on are in [packages/starter-pack](../starter-pack/README.md).

## 15. Changing the format

- A change that could make a valid form invalid, or change what a valid form means, needs a new `formatVersion`. Published forms keep the format they were written in.
- Change [form.schema.json](form.schema.json), both validators and this document together, and add a broken example for every new rule.
- If the schema needs a JSON Schema keyword the validators do not know yet, add it to both validators with the same message.
