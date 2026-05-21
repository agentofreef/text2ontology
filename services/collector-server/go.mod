module github.com/lakehouse2ontology/services/collector-server

go 1.25.0

require (
	github.com/google/uuid v1.6.0
	github.com/lakehouse2ontology/authmw v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/contracts v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/dsnguard v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/httputil v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/llmclient v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/observability v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/srvkit v0.0.0-00010101000000-000000000000
	github.com/lib/pq v1.10.9
	github.com/xuri/excelize/v2 v2.10.1
	golang.org/x/text v0.34.0
	modernc.org/sqlite v1.50.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.7 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/prometheus/client_golang v1.20.5 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/richardlehane/mscfb v1.0.6 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.40.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.40.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.40.0 // indirect
	go.opentelemetry.io/otel/metric v1.40.0 // indirect
	go.opentelemetry.io/otel/sdk v1.40.0 // indirect
	go.opentelemetry.io/otel/trace v1.40.0 // indirect
	go.opentelemetry.io/proto/otlp v1.9.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260128011058-8636f8732409 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260128011058-8636f8732409 // indirect
	google.golang.org/grpc v1.78.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	modernc.org/libc v1.72.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace (
	github.com/lakehouse2ontology/authmw => ../../pkg/authmw
	github.com/lakehouse2ontology/contracts => ../../pkg/contracts
	github.com/lakehouse2ontology/dsnguard => ../../pkg/dsnguard
	github.com/lakehouse2ontology/httputil => ../../pkg/httputil
	github.com/lakehouse2ontology/llmclient => ../../pkg/llmclient
	github.com/lakehouse2ontology/observability => ../../pkg/observability
	github.com/lakehouse2ontology/srvkit => ../../pkg/srvkit
)
