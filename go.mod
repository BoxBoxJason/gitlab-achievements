module github.com/boxboxjason/gitlab-achievements

go 1.26.4

require (
	github.com/glebarez/sqlite v1.11.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	gitlab.com/gitlab-org/api/client-go/v2 v2.58.2
	go.uber.org/zap v1.28.0
	golang.org/x/oauth2 v0.36.0
	golang.org/x/time v0.15.0
	gorm.io/driver/mysql v1.6.0
	gorm.io/driver/postgres v1.6.0
	gorm.io/driver/sqlserver v1.6.3
	gorm.io/gorm v1.31.2
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/glebarez/go-sqlite v1.22.0 // indirect
	github.com/go-sql-driver/mysql v1.10.0 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.8 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/microsoft/go-mssqldb v1.10.0 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	modernc.org/libc v1.74.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.54.0 // indirect
)

// TODO: drop this replace once achievements/system-hooks support lands
// upstream (gitlab.com/gitlab-org/api/client-go, branch
// feat/achievements-service-support) and a release is cut.
replace gitlab.com/gitlab-org/api/client-go/v2 => gitlab.com/gitlab-community/gitlab-org/api/client-go/v2 v2.58.3-0.20260823201250-f170b2671359
