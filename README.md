# Kubernetes Fake Metrics ApiServer

This is an implementation of [k8s-sigs custom-metrics-apiserver](https://github.com/kubernetes-sigs/custom-metrics-apiserver).

It will emit fake k8s metrics for a given HPA and include a promethues histogram of how often the `kube-controller-manager/hpa-autoscaler` is querying the metrics server for metrics, allowing a path to see performance degredation of hpa at scale.