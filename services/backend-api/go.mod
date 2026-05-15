module github.com/lakehouse2ontology/services/backend-api

go 1.25.0

require (
	github.com/lakehouse2ontology/authmw v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/httputil v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/llmclient v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/observability v0.0.0-00010101000000-000000000000
	github.com/lakehouse2ontology/ontology v0.0.0-00010101000000-000000000000
	github.com/lib/pq v1.10.9
	golang.org/x/text v0.34.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.24.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.20.5 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel v1.33.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.33.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.33.0 // indirect
	go.opentelemetry.io/otel/metric v1.33.0 // indirect
	go.opentelemetry.io/otel/sdk v1.33.0 // indirect
	go.opentelemetry.io/otel/trace v1.33.0 // indirect
	go.opentelemetry.io/proto/otlp v1.4.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20241209162323-e6fa225c2576 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20241209162323-e6fa225c2576 // indirect
	google.golang.org/grpc v1.68.1 // indirect
	google.golang.org/protobuf v1.35.2 // indirect
)

replace (
	github.com/lakehouse2ontology/authmw => ../../pkg/authmw
	github.com/lakehouse2ontology/httputil => ../../pkg/httputil
	github.com/lakehouse2ontology/llmclient => ../../pkg/llmclient
	github.com/lakehouse2ontology/observability => ../../pkg/observability
	github.com/lakehouse2ontology/ontology => ../../pkg/ontology
)
