#!/bin/bash

set -eu

# GitHub Actions use docker
#apt-get -y install docker.io
gpasswd -a ubuntu docker

# runner file cache
cd /usr/local/etc
curl -O -L https://github.com/actions/runner/releases/download/v2.275.1/actions-runner-linux-x64-2.275.1.tar.gz
cd /root/
