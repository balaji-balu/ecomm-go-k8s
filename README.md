# ecomm-go-k8s

Ecommerce application
- app written in go with error handling, metrics collection, logging to a file
- use mysql, two nodes
- use redis for caching
- prometheus for metrics collection
- loki, promtail for log collection from app

## Docker_compose
docker compose build -up

## Kubernetes manifests
cd manifests
kubectl apply -f namespace.yaml
kubectl apply -f configmap.yaml
kubectl apply -f secret.yaml
kubectl apply -f app-deployment.yaml
kubectl apply -f redis-deployment.yaml
kubectl apply -f db1-deployment.yaml
kubectl apply -f db2-deployment.yaml
kubectl apply -f prometheus-deployment.yaml
kubectl apply -f loki-deployment.yaml
kubectl apply -f promtail-deployment.yaml
kubectl apply -f services.yaml
