module github.com/latebit/demarkus-library

go 1.26.4

require (
	github.com/labstack/echo/v5 v5.1.1
	github.com/latebit/demarkus/client v0.0.0
	github.com/latebit/demarkus/protocol v0.0.0
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/yuin/goldmark v1.7.8
	golang.org/x/net v0.53.0
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/quic-go/quic-go v0.59.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Phase 0 spike: point the transport at the local demarkus source tree.
// Replace with tagged versions once the client/protocol modules are published.
replace github.com/latebit/demarkus/client => ../demarkus/client

replace github.com/latebit/demarkus/protocol => ../demarkus/protocol
