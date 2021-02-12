module github.com/whywaita/myshoes-providers/shoes-lxd

go 1.15

require (
	github.com/flosch/pongo2 v0.0.0-00010101000000-000000000000 // indirect
	github.com/gorilla/websocket v1.4.2 // indirect
	github.com/hashicorp/go-plugin v1.4.0
	github.com/lxc/lxd v0.0.0-20210202231940-8ee13883eaba
	github.com/pborman/uuid v1.2.1 // indirect
	github.com/whywaita/myshoes v1.2.1
	google.golang.org/grpc v1.35.0
	gopkg.in/macaroon-bakery.v2 v2.2.0 // indirect
	gopkg.in/robfig/cron.v2 v2.0.0-20150107220207-be2e0b0deed5 // indirect
)

replace github.com/flosch/pongo2 => github.com/flosch/pongo2/v4 v4.0.2
