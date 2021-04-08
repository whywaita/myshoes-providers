# shoes-lxd

lxd shoes

## Setup

please set Environment values

### Required values

`shoes-lxd` needs information of worker.

use `LXD_HOSTS` and ignore `LXD_HOST` if set both.

#### for single worker

- `LXD_HOST`
  - FQDN or IP address of running LXD daemon
- `LXD_CLIENT_CERT`
  - File path of client cert
- `LXD_CLIENT_KEY`
  - File path of client cert key

#### for multi worker

- `LXD_HOSTS`

```json
[
  {
    "host": "https://192.0.2.100:8443",
    "client_cert": "./node1/client.crt",
    "client_key": "./node1/client.key"
  },
  ...
]
```

### Optional values
- `LXD_IMAGE_ALIAS`
  - set runner image alias
  - default: `ubuntu:bionic`
  - e.g.) `ubuntu:focal`, `192.0.2.100:ubuntu-custom`
- `LXD_RESOURCE_TYPE_MAPPING`
  - mapping `resource_type` and CPU / Memory.
  - need JSON format. keys is `resource_type_name`, `cpu`, `memory`.
  - e.g.) `[{"resource_type_name": "nano", "cpu": 1, "memory": "1GB"}, {"resource_type_name": "micro", "cpu": 2, "memory": "2GB"}]`
  - become no limit if not set resource_type.

## Note
LXD Server can't use `zfs` in storageclass if use `--privileged`. ref: https://discuss.linuxcontainers.org/t/docker-with-overlay-driver-in-lxd-cluster-not-working/9243

We recommend using `btrfs`.