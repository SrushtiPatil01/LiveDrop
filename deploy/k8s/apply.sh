#!/bin/sh
set -eu

: "${ACM_CERTIFICATE_ARN:?Set ACM_CERTIFICATE_ARN to the ALB TLS certificate ARN}"

kubectl apply -f deploy/k8s/config.yaml
kubectl apply -f deploy/k8s/monitoring.yaml
envsubst '${ACM_CERTIFICATE_ARN}' < deploy/k8s/ingress.yaml.tmpl | kubectl apply -f -
