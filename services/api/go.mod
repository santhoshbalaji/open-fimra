module github.com/santhoshbalaji/open-fimra/services/api

go 1.26.0

require (
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/jackc/pgx/v5 v5.13.0
	github.com/riverqueue/river v0.51.2
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.51.2
)

require github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/riverqueue/river/riverdriver v0.51.2 // indirect
	github.com/riverqueue/river/rivershared v0.51.2 // indirect
	github.com/riverqueue/river/rivertype v0.51.2 // indirect
	github.com/santhoshbalaji/open-fimra/packages/form-schema v0.0.0
	github.com/santhoshbalaji/open-fimra/packages/starter-pack v0.0.0
	github.com/santhoshbalaji/open-fimra/packages/state-extract v0.0.0
	github.com/tidwall/gjson v1.20.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/santhoshbalaji/open-fimra/packages/form-schema => ../../packages/form-schema

replace github.com/santhoshbalaji/open-fimra/packages/starter-pack => ../../packages/starter-pack

replace github.com/santhoshbalaji/open-fimra/packages/state-extract => ../../packages/state-extract
