# Multi-Cluster Management

By default Multi Cluster Managmement is disabled in Rancher.  To enable set the
following in the rancherd config.yaml
```yaml
#cloud-config
rancherd:
  rancherValues:
    features:
    - multi-cluster-management=true
```
