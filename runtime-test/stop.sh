pushd $(dirname "$0") > /dev/null

kubectl delete -f deploy/hivedscheduler.yaml
kubectl delete -f deploy/hivedscheduler-service.yaml
kubectl delete -f deploy/rbac.yaml
kubectl delete -f deploy/hivedscheduler-config.yaml

popd > /dev/null
