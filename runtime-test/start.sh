set -o errexit
set -o nounset
set -o pipefail

pushd $(dirname "$0") > /dev/null

kubectl apply --overwrite=true -f deploy/hivedscheduler-config.yaml
kubectl apply --overwrite=true -f deploy/rbac.yaml
kubectl apply --overwrite=true -f deploy/hivedscheduler-service.yaml
kubectl apply --overwrite=true -f deploy/hivedscheduler.yaml

popd > /dev/null
