# Form schema

The published form format and its validators, shared by the web form designer, the mobile app and the core API.

| File | What it is |
|---|---|
| [SPEC.md](SPEC.md) | The specification, in plain English. Start here. |
| [form.schema.json](form.schema.json) | The JSON Schema for a form definition. This is the one copy. |
| `*.go` | The Go validator, for the core API. |
| [ts/form-schema.ts](ts/form-schema.ts) | The TypeScript validator, for the web app. |
| [examples/](examples) | Valid example forms, including drafts of the five starter-pack forms for the fictional Demo State. |
| [examples/columns/](examples/columns) | The columns each valid example produces. |
| [examples/invalid/](examples/invalid) | Forms broken in one way each, with the message both validators must give. |

## Testing

```bash
make web-install        # once: the type check uses the web app's TypeScript
make form-schema-test   # Go tests, Node.js tests and the TypeScript type check
```

Both validators run every file in `examples/`. After changing a valid example, rewrite its columns file with `FORM_SCHEMA_WRITE_COLUMNS=1 go test ./...` in this folder and check the difference before committing. The TypeScript test then confirms that it gives the same columns.

## Using the Go package

This folder is its own Go module, `github.com/santhoshbalaji/open-fimra/packages/form-schema`, with no dependencies. The core API adds it with a local replacement, so both always build from the same checkout:

```
require github.com/santhoshbalaji/open-fimra/packages/form-schema v0.0.0
replace github.com/santhoshbalaji/open-fimra/packages/form-schema => ../../packages/form-schema
```

```go
problems := formschema.Validate(data) // empty when the form is valid
columns, _ := formschema.Columns(data) // nil when the form is not valid
```

The package embeds `form.schema.json` with `go:embed`. Go can embed only files at or below the package folder, which is why the Go files sit here beside the schema rather than in `services/api`. There is no second copy to keep in step.

## Using the TypeScript module

The web app maps `@open-fimra/form-schema` to `ts/form-schema.ts` in its `tsconfig.json`:

```ts
import { formColumns, validateForm } from '@open-fimra/form-schema';
```

The module imports `form.schema.json` directly and has no dependencies. It uses only type syntax that Node.js can strip, so Node.js 24 runs it and its tests without a build step. It does not use a general JSON Schema library: those word their messages differently from each other, and the format promises the same message from Go and TypeScript.
