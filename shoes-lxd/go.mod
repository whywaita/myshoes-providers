module github.com/whywaita/myshoes-provider/shoes-lxd

go 1.15

require (
	github.com/flosch/pongo2 v0.0.0-00010101000000-000000000000 // indirect
	github.com/flosch/pongo2/v4 v4.0.2 // indirect
	github.com/gorilla/websocket v1.4.2 // indirect
	github.com/hashicorp/go-plugin v1.4.0
	github.com/lxc/lxd v0.0.0-20201022204652-7b6b82fe4d71
	github.com/pkg/errors v0.9.1 // indirect
	github.com/whywaita/myshoes v0.0.0-20201208152330-63914e02aab5
	google.golang.org/grpc v1.33.2
	gopkg.in/macaroon-bakery.v2 v2.2.0 // indirect
	gopkg.in/robfig/cron.v2 v2.0.0-20150107220207-be2e0b0deed5 // indirect
)

replace github.com/flosch/pongo2 => github.com/flosch/pongo2/v4 v4.0.0
