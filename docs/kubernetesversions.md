# Kubernetes versions
## Valid versions

The list of valid versions for the `kubernetesVersion` field can be determined
from the Rancher metadata using the following commands.

__k3s:__
```bash
curl -sL https://raw.githubusercontent.com/rancher/kontainer-driver-metadata/release-v2.6/data/data.json | jq -r '.k3s.releases[].version'
```
__rke2:__
```bash
curl -sL https://raw.githubusercontent.com/rancher/kontainer-driver-metadata/release-v2.6/data/data.json | jq -r '.rke2.releases[].version'
```
