set -o errexit
set -o nounset
set -o pipefail

pushd $(dirname "$0") > /dev/null

bash stop.sh

bash start.sh

popd > /dev/null
